package pageops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dakolli/webmcp/pkg/browsers"
)

// maxInjectElements caps the element list a set_html result returns; nodes_added
// and scripts keep counting the whole write.
const maxInjectElements = 50

// setAttrResult is the page_act set_attr payload. Removed reports that an empty
// value deleted the attribute. URLDelta is emitted only when the write changed
// the top-level URL, so a plain attribute keeps the five-field shape.
type setAttrResult struct {
	Target     string `json:"target"`
	Name       string `json:"name"`
	Value      string `json:"value"`
	Removed    bool   `json:"removed"`
	NodesAdded int    `json:"nodes_added"`
	URLDelta   bool   `json:"url_delta,omitempty"`
}

// setHTMLResult is the page_act set_html payload: the mode, the nodes the write
// created, the top-level elements (capped), the script count and whether the
// tracer saw a hit beyond the write itself.
type setHTMLResult struct {
	Target     string           `json:"target"`
	Mode       string           `json:"mode"`
	NodesAdded int              `json:"nodes_added"`
	Elements   []setHTMLElement `json:"elements"`
	Scripts    int              `json:"scripts"`
	Executed   bool             `json:"executed"`
}

// setHTMLElement is one top-level element a set_html write created.
type setHTMLElement struct {
	Tag        string            `json:"tag"`
	Attributes map[string]string `json:"attributes"`
}

// setAttrScript sets or removes one attribute and counts the nodes the write
// created. A srcdoc frame loads a document the parent observer cannot see, so
// the frame itself is counted; an attribute-only write adds no node.
const setAttrScript = `(name, value) => {
	const el = this;
	const url = location.href;
	const obs = new MutationObserver(() => {});
	obs.observe(document, {childList: true, subtree: true});
	let removed = false;
	if (value === '') {
		removed = true;
		el.removeAttribute(name);
	} else {
		el.setAttribute(name, value);
	}
	const recs = obs.takeRecords();
	obs.disconnect();
	let nodes = 0;
	for (const rec of recs) nodes += rec.addedNodes.length;
	const lower = name.toLowerCase();
	if (value !== '' && lower === 'srcdoc' && (el.localName === 'iframe' || el.localName === 'frame')) nodes += 1;
	return {removed, nodes_added: nodes, url_delta: location.href !== url};
}`

// setHTMLScript is the set_html template. The tracer records the write itself
// as one hit, so executed is true only for a further hit the write triggered.
const setHTMLScriptTemplate = `(markup, mode) => {
	const el = this;
	const sinkTotal = () => window.__wmSinkRing
		? (window.__wmSinkRing.dropped || 0) + window.__wmSinkRing.hits.length
		: 0;
	const before = sinkTotal();
	const obs = new MutationObserver(() => {});
	obs.observe(document, {childList: true, subtree: true});
	if (mode === 'outer') {
		el.outerHTML = markup;
	} else if (mode === 'append') {
		el.insertAdjacentHTML('beforeend', markup);
	} else {
		el.innerHTML = markup;
	}
	const recs = obs.takeRecords();
	obs.disconnect();
	const after = sinkTotal();
	const countTree = (node) => {
		let n = 1;
		if (node.nodeType === 1) {
			for (const child of node.childNodes) n += countTree(child);
		}
		return n;
	};
	let nodes = 0;
	const added = [];
	const seen = new Set();
	for (const rec of recs) {
		for (const node of rec.addedNodes) {
			if (seen.has(node)) continue;
			seen.add(node);
			nodes += countTree(node);
			if (node.nodeType === 1) added.push(node);
		}
	}
	const elements = [];
	for (let i = 0; i < added.length && elements.length < %d; i++) {
		const attrs = Object.create(null);
		for (const attr of added[i].attributes) attrs[attr.name] = attr.value;
		elements.push({tag: added[i].localName, attributes: attrs});
	}
	let scripts = 0;
	for (const node of added) {
		if (node.localName === 'script') scripts += 1;
		scripts += node.querySelectorAll('script').length;
	}
	return {nodes_added: nodes, elements, scripts, executed: after - before > 1};
}`

// setHTMLScript is the executable set_html script with the element cap filled
// from the Go constant so the page and the result cannot drift.
var setHTMLScript = fmt.Sprintf(setHTMLScriptTemplate, maxInjectElements)

// SetHTMLMode normalises a set_html mode, defaulting an empty mode to inner.
// The accepted values map to innerHTML, outerHTML and insertAdjacentHTML.
func SetHTMLMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "inner":
		return "inner", nil
	case "outer":
		return "outer", nil
	case "append":
		return "append", nil
	default:
		return "", fmt.Errorf("unknown mode %q (must be inner, outer or append)", mode)
	}
}

// PageSetAttr sets one attribute on the element targeted by handle or selector.
// An empty value removes the attribute. There is no allowlist: javascript: and
// onerror values reach the page unchanged. The result reports the target, the
// attribute, the value, whether it was removed, and the nodes the write created.
func PageSetAttr(
	ctx context.Context,
	p *browsers.Page,
	handle, selector, name, value string,
) (string, error) {
	return browsers.GuardPage(ctx, p, "set_attr", func(ctx context.Context) (string, error) {
		if p.Rod == nil {
			return "", fmt.Errorf("page session is closed")
		}
		if name == "" {
			return "", fmt.Errorf("set_attr: name is required (the attribute to set, e.g. href)")
		}

		page := p.Rod.Context(ctx)
		el, err := actTarget(page, handle, selector)
		if err != nil {
			return "", actionError("set_attr", handle, selector, err)
		}

		if err := p.PauseAction(ctx, "set_attr"); err != nil {
			return "", err
		}

		res, err := el.Eval(setAttrScript, name, value)
		if err != nil {
			return "", targetFailure("set_attr", handle, selector, err)
		}
		var got struct {
			Removed    bool `json:"removed"`
			NodesAdded int  `json:"nodes_added"`
			URLDelta   bool `json:"url_delta"`
		}
		if err := res.Value.Unmarshal(&got); err != nil {
			return "", fmt.Errorf("set_attr: decode result: %w", err)
		}

		if err := p.PauseAction(ctx, "set_attr"); err != nil {
			return "", err
		}

		result := setAttrResult{
			Target:     styleTarget(handle, selector),
			Name:       name,
			Value:      value,
			Removed:    got.Removed,
			NodesAdded: got.NodesAdded,
			URLDelta:   got.URLDelta,
		}
		b, err := json.Marshal(result)
		if err != nil {
			return "", fmt.Errorf("set_attr: encode result: %w", err)
		}
		return string(b), nil
	})
}

// PageSetHTML writes markup into the element targeted by handle or selector.
// Mode selects innerHTML ("inner", the default), outerHTML ("outer") or
// insertAdjacentHTML("beforeend") ("append"). There is no allowlist and no
// sanitisation. The result lists the created top-level elements, counts the
// created nodes and scripts, and reports executed only when the tracer recorded
// a hit beyond the write itself.
func PageSetHTML(
	ctx context.Context,
	p *browsers.Page,
	handle, selector, value, mode string,
) (string, error) {
	canonical, err := SetHTMLMode(mode)
	if err != nil {
		return "", fmt.Errorf("set_html: %w", err)
	}
	return browsers.GuardPage(ctx, p, "set_html", func(ctx context.Context) (string, error) {
		if p.Rod == nil {
			return "", fmt.Errorf("page session is closed")
		}

		page := p.Rod.Context(ctx)
		el, err := actTarget(page, handle, selector)
		if err != nil {
			return "", actionError("set_html", handle, selector, err)
		}

		if err := p.PauseAction(ctx, "set_html"); err != nil {
			return "", err
		}

		res, err := el.Eval(setHTMLScript, value, canonical)
		if err != nil {
			return "", targetFailure("set_html", handle, selector, err)
		}
		var got struct {
			NodesAdded int              `json:"nodes_added"`
			Elements   []setHTMLElement `json:"elements"`
			Scripts    int              `json:"scripts"`
			Executed   bool             `json:"executed"`
		}
		if err := res.Value.Unmarshal(&got); err != nil {
			return "", fmt.Errorf("set_html: decode result: %w", err)
		}

		if err := p.PauseAction(ctx, "set_html"); err != nil {
			return "", err
		}

		elements := got.Elements
		if elements == nil {
			elements = []setHTMLElement{}
		}
		for i := range elements {
			if elements[i].Attributes == nil {
				elements[i].Attributes = map[string]string{}
			}
		}
		result := setHTMLResult{
			Target:     styleTarget(handle, selector),
			Mode:       canonical,
			NodesAdded: got.NodesAdded,
			Elements:   elements,
			Scripts:    got.Scripts,
			Executed:   got.Executed,
		}
		b, err := json.Marshal(result)
		if err != nil {
			return "", fmt.Errorf("set_html: encode result: %w", err)
		}
		return string(b), nil
	})
}

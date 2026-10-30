package pageops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dakolli/webmcp/pkg/browsers"
	"github.com/go-rod/rod"
	"github.com/ysmood/gson"
)

const (
	// reflectionMaxHits bounds how many hits one read emits; the remainder is
	// reported through truncated.
	reflectionMaxHits = 200
	// reflectionSafetyHits bounds what the in-page probe collects, so a page
	// that reflects the marker into thousands of nodes cannot build an unbounded
	// string before the Go shaper caps it.
	reflectionSafetyHits = 500
	// reflectionWindow is the number of runes kept on each side of the marker in
	// one snippet.
	reflectionWindow = 40
)

// reflectionSpecials is the tracked character set, in report order.
var reflectionSpecials = []string{"<", ">", "\"", "'", "&", "/"}

// reflectionEntityFor maps one special character to its entity spellings.
var reflectionEntityFor = map[string][]string{
	"<":  {"&lt;", "&#60;", "&#x3c;", "&#x3C;"},
	">":  {"&gt;", "&#62;"},
	"\"": {"&quot;", "&#34;"},
	"'":  {"&#39;", "&apos;", "&#x27;", "&#X27;"},
	"&":  {"&amp;", "&#38;"},
	"/":  {"&#47;", "&sol;"},
}

// reflectionEntities flattens every entity spelling for the bare-ampersand and
// entity-form checks.
var reflectionEntities = func() []string {
	out := make([]string, 0, len(reflectionEntityFor)*3)
	for _, entities := range reflectionEntityFor {
		out = append(out, entities...)
	}
	return out
}()

// reflectionRaw is one raw hit the in-page probe returns. Quote is the
// delimiter the serializer placed around the hit: the attribute quote, the
// enclosing script quote, or empty. Raw is the serialized content around the
// marker with the enclosing tag and delimiter quotes removed.
type reflectionRaw struct {
	Where     string `json:"where"`
	Selector  string `json:"selector"`
	Attribute string `json:"attribute"`
	Quote     string `json:"quote"`
	Raw       string `json:"raw"`
}

// reflectionsProbe is the payload one in-page evaluation returns.
type reflectionsProbe struct {
	Marker string          `json:"marker"`
	URL    string          `json:"url"`
	Total  int             `json:"total"`
	Hits   []reflectionRaw `json:"hits"`
}

// reflectionHit is one classified reflection.
type reflectionHit struct {
	Where     string   `json:"where"`
	Selector  string   `json:"selector,omitempty"`
	Attribute string   `json:"attribute,omitempty"`
	Context   string   `json:"context"`
	Snippet   string   `json:"snippet"`
	Survived  []string `json:"survived"`
	Risk      string   `json:"risk"`
}

// correlatedSink is one tracer hit whose value carries the marker.
type correlatedSink struct {
	Sink     string `json:"sink"`
	Context  string `json:"context"`
	Selector string `json:"selector,omitempty"`
	Value    string `json:"value"`
}

// reflectionsResult is the wire shape of what=reflections.
type reflectionsResult struct {
	Marker string `json:"marker"`
	Count  int    `json:"count"`
	// Truncated counts the hits the cap withheld.
	Truncated       int              `json:"truncated"`
	Hits            []reflectionHit  `json:"hits"`
	SinksCorrelated []correlatedSink `json:"sinks_correlated,omitempty"`
}

// PageReflections hunts marker across the live DOM, the serialized
// document.documentElement.outerHTML and document.URL, then classifies every hit
// by context, surviving special characters and risk. It never mutates the page
// or sends a request. marker arrives through target and an optional selector
// scopes the scan to one subtree. A marker with no hits returns count:0, not an
// error. When the sink tracer is installed, the matching sink hits ride the
// result as sinks_correlated.
func PageReflections(
	ctx context.Context,
	p *browsers.Page,
	marker string,
	selector string,
) (string, error) {
	marker = strings.TrimSpace(marker)
	if marker == "" {
		return "", fmt.Errorf(
			"reflections requires target=<marker> — the string to hunt, e.g. target=wm-probe-1a2b",
		)
	}

	probe, err := probeReflections(ctx, p, marker, selector)
	if err != nil {
		return "", err
	}

	res := shapeReflections(probe)
	correlateReflections(ctx, p, marker, &res)

	b, err := json.Marshal(res)
	if err != nil {
		return "", fmt.Errorf("encode reflections: %w", err)
	}
	return string(b), nil
}

// probeReflections runs the one in-page scan under the page read hold and
// decodes its raw hits. A selector that matches nothing returns the shared
// element-not-found sentence.
func probeReflections(
	ctx context.Context,
	p *browsers.Page,
	marker string,
	selector string,
) (reflectionsProbe, error) {
	ctx, done, err := beginPage(ctx, p, "reflections")
	if err != nil {
		return reflectionsProbe{}, err
	}
	defer done()
	if p.Rod == nil {
		return reflectionsProbe{}, fmt.Errorf("page session is closed")
	}

	res, err := p.Rod.Context(ctx).Eval(reflectionsProbeJS, marker, selector, reflectionSafetyHits)
	if err != nil {
		return reflectionsProbe{}, fmt.Errorf("evaluate reflections: %w", err)
	}

	probe, err := decodeReflections(res.Value)
	if err != nil {
		return reflectionsProbe{}, err
	}
	if probe == nil {
		return reflectionsProbe{}, resolveError(selector, &rod.ElementNotFoundError{})
	}
	return *probe, nil
}

// decodeReflections decodes the in-page probe payload from the raw eval value,
// returning nil when the selector matched nothing. Unmarshal runs before any
// gson accessor: a single read consumes the raw bytes and a later Unmarshal then
// fails with "gson: value has been parsed".
func decodeReflections(value gson.JSON) (*reflectionsProbe, error) {
	var probe *reflectionsProbe
	if err := value.Unmarshal(&probe); err != nil {
		return nil, fmt.Errorf("decode reflections: %w", err)
	}
	return probe, nil
}

// correlateReflections attaches the tracer hits whose value carries the marker.
// It runs after the read hold is released because SinkHits takes the page lock
// itself. An uninstalled tracer or a failed read is not an error for this
// aspect.
func correlateReflections(
	ctx context.Context,
	p *browsers.Page,
	marker string,
	res *reflectionsResult,
) {
	sinks, err := p.SinkHits(ctx)
	if err != nil || !sinks.Installed {
		return
	}
	out := make([]correlatedSink, 0)
	for _, h := range sinks.Hits {
		if !strings.Contains(h.Value, marker) {
			continue
		}
		out = append(out, correlatedSink{
			Sink:     h.Sink,
			Context:  h.Context,
			Selector: h.Selector,
			Value:    h.Value,
		})
	}
	if len(out) > 0 {
		res.SinksCorrelated = out
	}
}

// shapeReflections applies the hit cap and classifies every emitted raw hit.
func shapeReflections(probe reflectionsProbe) reflectionsResult {
	out := reflectionsResult{
		Marker: probe.Marker,
		Hits:   make([]reflectionHit, 0, len(probe.Hits)),
	}
	hits := probe.Hits
	if len(hits) > reflectionMaxHits {
		hits = hits[:reflectionMaxHits]
	}
	for _, raw := range hits {
		out.Hits = append(out.Hits, shapeReflectionHit(probe.Marker, raw))
	}
	out.Count = len(out.Hits)
	out.Truncated = probe.Total - out.Count
	if out.Truncated < 0 {
		out.Truncated = 0
	}
	return out
}

// shapeReflectionHit classifies one raw hit from its serialized snippet.
func shapeReflectionHit(marker string, raw reflectionRaw) reflectionHit {
	snippet := windowAround(raw.Raw, marker, reflectionWindow)
	contextName := classifyContext(raw.Where, raw.Quote)
	survived := survivedChars(marker, snippet)
	return reflectionHit{
		Where:     raw.Where,
		Selector:  raw.Selector,
		Attribute: raw.Attribute,
		Context:   contextName,
		Snippet:   snippet,
		Survived:  survived,
		Risk:      assessRisk(contextName, survived, entityEscaped(marker, snippet)),
	}
}

// windowAround returns at most windows runes on each side of the marker, adding
// a leading or trailing ellipsis when the source was clipped. A marker the
// source does not carry falls back to the leading window.
func windowAround(s, marker string, windows int) string {
	if s == "" {
		return ""
	}
	runes := []rune(s)
	idx := strings.Index(s, marker)
	if idx < 0 {
		if len(runes) > 2*windows {
			return string(runes[:2*windows]) + "…"
		}
		return s
	}
	mid := len([]rune(s[:idx]))
	span := len([]rune(marker))
	start := mid - windows
	if start < 0 {
		start = 0
	}
	end := mid + span + windows
	if end > len(runes) {
		end = len(runes)
	}
	pre := ""
	if start > 0 {
		pre = "…"
	}
	post := ""
	if end < len(runes) {
		post = "…"
	}
	return pre + string(runes[start:end]) + post
}

// classifyContext names the reflection context from the node kind and the
// delimiter the probe observed.
func classifyContext(where, quote string) string {
	switch where {
	case "url":
		return "url"
	case "comment":
		return "comment"
	case "style-text":
		return "css-value"
	case "script-text":
		if quote == "`" {
			return "js-template"
		}
		return "js-string"
	case "attr":
		switch quote {
		case "\"":
			return "attr-double"
		case "'":
			return "attr-single"
		default:
			return "attr-unquoted"
		}
	default:
		return "html-text"
	}
}

// markerSpecials lists the tracked characters the marker itself carries.
func markerSpecials(marker string) []string {
	out := make([]string, 0, len(reflectionSpecials))
	for _, c := range reflectionSpecials {
		if strings.Contains(marker, c) {
			out = append(out, c)
		}
	}
	return out
}

// survivedChars lists the marker's special characters that appear literally in
// the serialized snippet. A character the marker does not carry is never
// reported, so surrounding markup or delimiters cannot fake survival. An
// ampersand counts only when it does not open a known entity.
func survivedChars(marker, snippet string) []string {
	out := make([]string, 0, len(reflectionSpecials))
	for _, c := range markerSpecials(marker) {
		if c == "&" {
			if hasBareAmp(snippet) {
				out = append(out, c)
			}
			continue
		}
		if strings.Contains(snippet, c) {
			out = append(out, c)
		}
	}
	return out
}

// entityEscaped reports whether the snippet carries the entity spelling of a
// special character the marker carries.
func entityEscaped(marker, snippet string) bool {
	for _, c := range markerSpecials(marker) {
		for _, e := range reflectionEntityFor[c] {
			if strings.Contains(snippet, e) {
				return true
			}
		}
	}
	return false
}

// hasBareAmp reports an ampersand that does not begin a known entity.
func hasBareAmp(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '&' && !startsWithEntity(s[i:]) {
			return true
		}
	}
	return false
}

// startsWithEntity reports whether s opens with a known entity spelling.
func startsWithEntity(s string) bool {
	for _, e := range reflectionEntities {
		if strings.HasPrefix(s, e) {
			return true
		}
	}
	return false
}

// assessRisk labels one hit: dangerous when a break character survives in an
// executable context, escaped when an entity spelling is present, inert
// otherwise.
func assessRisk(contextName string, survived []string, entity bool) string {
	has := func(c string) bool {
		for _, s := range survived {
			if s == c {
				return true
			}
		}
		return false
	}

	dangerous := false
	switch contextName {
	case "html-text", "css-value":
		dangerous = has("<")
	case "js-string":
		dangerous = has("<") || has("'") || has("\"")
	case "js-template":
		dangerous = has("<")
	case "attr-double":
		dangerous = has("\"")
	case "attr-single":
		dangerous = has("'")
	case "attr-unquoted":
		dangerous = has("'") || has("\"")
	}
	if dangerous {
		return "dangerous"
	}
	if entity {
		return "escaped"
	}
	return "inert"
}

// reflectionsProbeJS walks the scoped DOM and the URL once, returning raw facts
// for the Go classifier: one hit per matching attribute, text node, script or
// style text node, comment and URL occurrence. It mutates nothing and sends no
// request; the one detached element turns text into its serialized form. Raw is
// the serialized content around the marker with the enclosing tag and delimiter
// quotes removed, and Quote is the observed delimiter.
const reflectionsProbeJS = `(marker, selector, limit) => {` + cssPathJS + `
  const SURROUND = 40;
  const root = selector ? document.querySelector(selector) : document.documentElement;
  if (!root) return null;

  const hits = [];
  let total = 0;
  const clip = (s, idx) => {
    const start = Math.max(0, idx - SURROUND);
    const end = Math.min(s.length, idx + marker.length + SURROUND);
    return (start > 0 ? '…' : '') + s.slice(start, end) + (end < s.length ? '…' : '');
  };
  const push = (where, el, attribute, quote, raw) => {
    total++;
    if (hits.length >= limit) return;
    const path = el && el.nodeType === 1 ? cssPath(el) : '';
    hits.push({ where: where, selector: path, attribute: attribute || '', quote: quote || '', raw: raw });
  };

  const url = document.URL;
  const urlIdx = url.indexOf(marker);
  if (urlIdx >= 0) push('url', null, '', '', clip(url, urlIdx));

  const enc = document.createElement('div');
  const tagEnd = (html) => {
    let quote = '';
    for (let i = 0; i < html.length; i++) {
      const ch = html[i];
      if (quote) { if (ch === quote) quote = ''; }
      else if (ch === '"' || ch === "'") quote = ch;
      else if (ch === '>') return i + 1;
    }
    return html.length;
  };
  const attrParts = (el, name) => {
    const html = el.outerHTML || '';
    const tag = html.slice(0, tagEnd(html));
    let i = tag.indexOf(' ' + name + '=');
    if (i < 0) i = tag.indexOf(name + '=');
    if (i < 0) return { quote: '', value: '' };
    const eq = tag.indexOf('=', i);
    if (eq < 0) return { quote: '', value: '' };
    let j = eq + 1;
    let quote = '';
    if (tag[j] === '"' || tag[j] === "'") { quote = tag[j]; j++; }
    const end = quote ? tag.indexOf(quote, j) : tag.indexOf('>', j);
    return { quote: quote, value: tag.slice(j, end < 0 ? tag.length : end) };
  };

  const nearestQuote = (s, idx) => {
    const before = s.slice(0, idx);
    const tick = before.lastIndexOf('\x60');
    const plain = Math.max(before.lastIndexOf("'"), before.lastIndexOf('"'));
    if (tick > plain) return '\x60';
    if (plain >= 0) return before[plain];
    return '';
  };

  const walker = document.createTreeWalker(
    root,
    NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT | NodeFilter.SHOW_COMMENT,
    null
  );
  let node = walker.currentNode;
  while (node) {
    if (node.nodeType === 1 && node.attributes) {
      for (let k = 0; k < node.attributes.length; k++) {
        const attr = node.attributes[k];
        if (!attr.value || attr.value.indexOf(marker) < 0) continue;
        const parts = attrParts(node, attr.name);
        const idx = parts.value.indexOf(marker);
        const raw = idx >= 0 ? clip(parts.value, idx) : parts.value.slice(0, 400);
        push('attr', node, attr.name, parts.quote, raw);
      }
    } else if (node.nodeType === 3 && node.data && node.data.indexOf(marker) >= 0) {
      const parent = node.parentElement;
      const tag = parent ? parent.tagName.toLowerCase() : '';
      if (tag === 'script' || tag === 'style') {
        const idx = node.data.indexOf(marker);
        push(tag === 'script' ? 'script-text' : 'style-text', parent, '', nearestQuote(node.data, idx), clip(node.data, idx));
      } else {
        enc.textContent = node.data;
        const serialized = enc.innerHTML;
        const idx = serialized.indexOf(marker);
        push('dom-text', parent, '', '', idx >= 0 ? clip(serialized, idx) : serialized.slice(0, 400));
      }
    } else if (node.nodeType === 8 && node.data && node.data.indexOf(marker) >= 0) {
      const idx = node.data.indexOf(marker);
      push('comment', node.parentElement, '', '', clip(node.data, idx));
    }
    node = walker.nextNode();
  }
  enc.textContent = '';

  return { marker: marker, url: url, total: total, hits: hits };
}`

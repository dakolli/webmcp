package browsers

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/go-rod/rod/lib/proto"
)

// MaxInteractives caps how many interactive elements a single interactives
// snapshot returns. Pages with more interactives report the remainder via
// InteractivesResult.Truncated, so the LLM knows the action map is not
// complete and can scroll/paginate to reach the rest.
const MaxInteractives = 200

// Interactive is one clickable/typeable element in an interactives snapshot:
// the dense action-map entry an agent uses to act immediately via
// page_act { handle: ... } without selector guesswork.
type Interactive struct {
	// Handle is the stable element handle "b<backendNodeId>" (the WEB-51
	// scheme, minted from the snapshot's BackendNodeID parallel array — stable
	// across snapshot calls while the node lives in the renderer); usable
	// directly as page_act's handle arg. Omitted when the snapshot carried no
	// backend node id for the element.
	Handle string `json:"handle"`
	// Role is the element's ARIA role attribute, else its lowercased tag
	// (a/button/input/select/textarea/…).
	Role string `json:"role"`
	// Text is the element's layout text, or its TextValue, or the
	// aria-label → title → placeholder attribute fallback — whitespace-
	// normalized and capped at 80 chars. Omitted when empty.
	Text string `json:"text,omitempty"`
	// Href is the absolute href resolved against the document URL (links
	// only, mirroring a.href semantics). Omitted on non-links.
	Href string `json:"href,omitempty"`
	// Type is the input's type attribute (inputs only). Omitted on
	// non-inputs.
	Type string `json:"type,omitempty"`
	// X is the center x of the element's layout bounds, page-absolute CSS px.
	X float64 `json:"x"`
	// Y is the center y of the element's layout bounds, page-absolute CSS px.
	Y float64 `json:"y"`
	// Visible is always true: hidden/inert elements are skipped before the
	// snapshot is built, so every reported entry is rendered and interactable.
	Visible bool `json:"visible"`
}

// InteractivesResult is the wire shape of an interactives snapshot: the
// ordered list of interactive elements plus how many more were omitted by the
// MaxInteractives cap.
type InteractivesResult struct {
	// Interactives is the page's interactive elements in document order,
	// never null on the wire ([] on an empty page).
	Interactives []Interactive `json:"interactives"`
	// Truncated is the count of interactives beyond MaxInteractives that were
	// omitted (0 when none).
	Truncated int `json:"truncated"`
}

// Interactives returns the page's interactive elements as a dense action map:
// every visible, non-inert clickable/typeable element (anchors, buttons,
// inputs, selects, textareas, ARIA-role widgets, tabindex/onclick/
// contenteditable elements) with a WEB-51 handle, role, truncated text, href,
// input type and center x/y — the whole-page snapshot an agent uses to act
// immediately (page_act { handle: ... }) without selector guesswork.
//
// The method takes the page lock itself (mirroring Page.Close): callers must
// NOT pre-lock. The nil-Rod guard runs UNDER the lock and returns the exact
// pinned "page session is closed" error. The snapshot is ONE
// CaptureDOMSnapshot round trip (the same data source as the dom extractor),
// then a purpose-built linear walk of the root document's nodes in document
// order (later snapshot documents are iframes — for a first cut only the root
// document is walked; the minted handles are renderer-global backendNodeIds,
// so they remain valid for page_act regardless). No JS is evaluated: the
// snapshot's layout tree only contains rendered nodes, so the visibility
// filter is more accurate than an offsetParent heuristic (it does not wrongly
// exclude visible position:fixed elements).
//
// The walk applies the candidacy rules (tag/role/tabindex/onclick/
// contenteditable), skips inert elements (disabled, aria-disabled="true",
// aria-hidden="true" on the node or any ancestor) and unrendered nodes (no
// layout entry), collapses nested interactives to the outermost, and caps the
// result at MaxInteractives entries with the remainder reported via
// InteractivesResult.Truncated. ctx is threaded through to the rod call so a
// hung page cannot outlive the caller's deadline.
func (p *Page) Interactives(ctx context.Context) (InteractivesResult, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return InteractivesResult{}, fmt.Errorf("page session is closed")
	}
	snap, err := p.Rod.Context(ctx).CaptureDOMSnapshot() // error form; no Must*
	if err != nil {
		return InteractivesResult{}, fmt.Errorf("capture interactives snapshot: %w", err)
	}
	res, err := decodeInteractives(snap)
	if err != nil {
		return InteractivesResult{}, fmt.Errorf("decode interactives: %w", err)
	}
	return res, nil
}

// decodeInteractives walks the root document of a DOM snapshot in document
// order and collects every visible, non-inert interactive element into an
// InteractivesResult. It is a pure function over the snapshot structs (no
// rod), so it is unit-testable without Chromium: an empty-but-valid document
// yields the empty result (never nil), while a snapshot with no root document
// at all is a decode failure.
func decodeInteractives(snap *proto.DOMSnapshotCaptureSnapshotResult) (InteractivesResult, error) {
	res := InteractivesResult{Interactives: []Interactive{}}
	if snap == nil || len(snap.Documents) == 0 || snap.Documents[0] == nil {
		return res, fmt.Errorf("snapshot has no root document")
	}
	doc := snap.Documents[0]
	n := doc.Nodes
	if n == nil || len(n.ParentIndex) == 0 {
		return res, nil
	}

	strs := snap.Strings
	layout := layoutIndex(doc.Layout)

	// Pre-parse every element node's attributes once: candidacy, the inert
	// filters and the aria-hidden walk are pure attr lookups afterwards.
	attrsByNode := make([]map[string]string, len(n.ParentIndex))
	for i := range n.ParentIndex {
		if i < len(n.NodeType) && n.NodeType[i] == 1 {
			attrsByNode[i] = nodeAttrMap(strs, n.Attributes, i)
		}
	}

	// accepted holds every interactive already accounted for — emitted into
	// the result OR counted into Truncated — so nested descendants collapse
	// to the outermost candidate regardless of the cap.
	accepted := make(map[int]bool, MaxInteractives)
	for i := range n.ParentIndex {
		if i >= len(n.NodeType) || n.NodeType[i] != 1 {
			continue // elements only
		}
		tag := nodeTag(strs, n, i)
		attrs := attrsByNode[i]
		if !isInteractiveCandidate(tag, attrs) {
			continue
		}
		// Inert: a disabled attribute (any value) or aria-disabled="true".
		if _, has := attrs["disabled"]; has {
			continue
		}
		if strings.EqualFold(attrs["aria-disabled"], "true") {
			continue
		}
		// Unrendered nodes (display:none / detached) never appear in the
		// layout tree — a layout entry is the visibility requirement.
		li, hasLayout := layout[i]
		if !hasLayout {
			continue
		}
		// aria-hidden="true" on the node or any ancestor hides the whole
		// subtree — the action map must not include it.
		if hiddenOrAncestorHidden(n.ParentIndex, attrsByNode, i) {
			continue
		}
		// Nested collapse: a candidate inside an already-accounted-for
		// interactive collapses to the outermost (<a><button> yields the <a>).
		if ancestorAccepted(n.ParentIndex, i, accepted) {
			continue
		}
		if len(res.Interactives) >= MaxInteractives {
			accepted[i] = true
			res.Truncated++
			continue
		}
		accepted[i] = true
		res.Interactives = append(res.Interactives, buildInteractive(strs, doc, i, tag, attrs, li))
	}
	return res, nil
}

// isInteractiveCandidate reports whether an element node qualifies as an
// interactive. The candidacy rules are an OR chain: tag a-with-href,
// button/select/textarea, input (skip type=hidden), OR a widget ARIA role
// (button|link|checkbox|radio|switch|menuitem|tab), OR a tabindex attribute
// other than -1, OR an onclick handler, OR a contenteditable attribute. The
// tag rules only feed the chain — a tag never short-circuits the attribute
// rules, so an <a> without an href can still qualify via role/tabindex/onclick/
// contenteditable (e.g. <a role="button">), and an input whose type is not
// hidden qualifies by tag. Everything here is read from the snapshot's
// attributes — no JS is evaluated.
func isInteractiveCandidate(tag string, attrs map[string]string) bool {
	// tagCandidate is the tag clause of the OR chain. The a and input cases do
	// NOT return early: without an href (a) or with type=hidden (input) they
	// simply contribute no tag vote, leaving the attribute clauses below to
	// decide.
	var tagCandidate bool
	switch tag {
	case "a":
		_, tagCandidate = attrs["href"]
	case "button", "select", "textarea":
		tagCandidate = true
	case "input":
		tagCandidate = !strings.EqualFold(attrs["type"], "hidden")
	}
	if tagCandidate {
		return true
	}
	if isWidgetRole(attrs["role"]) {
		return true
	}
	if ti, ok := attrs["tabindex"]; ok && ti != "-1" {
		return true
	}
	if _, ok := attrs["onclick"]; ok {
		return true
	}
	if _, ok := attrs["contenteditable"]; ok {
		return true
	}
	return false
}

// isWidgetRole reports whether the ARIA role is in the widget set the action
// map tracks: roles the agent can click or otherwise drive.
func isWidgetRole(role string) bool {
	switch strings.ToLower(role) {
	case "button", "link", "checkbox", "radio", "switch", "menuitem", "tab":
		return true
	}
	return false
}

// hiddenOrAncestorHidden reports whether node i or any of its ancestors (via
// ParentIndex) carries aria-hidden="true" — such a subtree is hidden from
// assistive tech and must not appear in the action map.
func hiddenOrAncestorHidden(parents []int, attrsByNode []map[string]string, i int) bool {
	for a := i; a >= 0 && a < len(parents); {
		if strings.EqualFold(attrsByNode[a]["aria-hidden"], "true") {
			return true
		}
		parent := parents[a]
		if parent < 0 || parent >= len(parents) || parent == a {
			break
		}
		a = parent
	}
	return false
}

// ancestorAccepted reports whether any ancestor of node i (via ParentIndex) is
// already in the accepted set — the nested-collapse rule: a candidate nested
// inside an already-emitted (or truncated) interactive collapses to the
// outermost.
func ancestorAccepted(parents []int, i int, accepted map[int]bool) bool {
	for a := parents[i]; a >= 0 && a < len(parents) && a != i; a = parents[a] {
		if accepted[a] {
			return true
		}
	}
	return false
}

// buildInteractive assembles one Interactive entry from the node's snapshot
// rows: the WEB-51 handle (guarded parallel-array read), the ARIA-role-else-
// tag role, the text with its fallback chain, the resolved href (links), the
// input type (inputs) and the center of the layout bounds. li is the node's
// layout-tree entry index (always valid for accepted candidates).
func buildInteractive(
	strs []string,
	doc *proto.DOMSnapshotDocumentSnapshot,
	i int,
	tag string,
	attrs map[string]string,
	li int,
) Interactive {
	it := Interactive{
		Role:    roleOf(tag, attrs),
		Text:    textOf(strs, doc, i, li, attrs),
		Visible: true,
	}
	// Mirror WEB-51's buildDOMTree hoist exactly: the BackendNodeID parallel
	// array is aligned with the node table — guard the bounds and skip when
	// the id is absent or zero.
	n := doc.Nodes
	if i < len(n.BackendNodeID) && n.BackendNodeID[i] > 0 {
		it.Handle = fmt.Sprintf("b%d", n.BackendNodeID[i])
	}
	if tag == "a" {
		it.Href = resolveHref(attrs["href"], str(strs, doc.DocumentURL))
	}
	if tag == "input" {
		it.Type = attrs["type"]
	}
	if li >= 0 && li < len(doc.Layout.Bounds) && len(doc.Layout.Bounds[li]) >= 4 {
		b := doc.Layout.Bounds[li]
		it.X = b[0] + b[2]/2
		it.Y = b[1] + b[3]/2
	}
	return it
}

// roleOf returns the element's ARIA role attribute, else its lowercased tag.
func roleOf(tag string, attrs map[string]string) string {
	if r := attrs["role"]; r != "" {
		return r
	}
	return tag
}

// textOf returns the element's text for the action map: the layout text, else
// the textarea TextValue, else the aria-label → title → placeholder attribute
// fallback — whitespace-normalized and capped at 80 chars.
func textOf(strs []string, doc *proto.DOMSnapshotDocumentSnapshot, i, li int, attrs map[string]string) string {
	text := ""
	if li >= 0 && li < len(doc.Layout.Text) {
		text = str(strs, doc.Layout.Text[li])
	}
	if text == "" {
		text = rareString(strs, doc.Nodes.TextValue, i)
	}
	for _, k := range []string{"aria-label", "title", "placeholder"} {
		if text == "" {
			text = attrs[k]
		}
	}
	text = strings.Join(strings.Fields(text), " ") // whitespace-normalized
	if r := []rune(text); len(r) > 80 {
		text = string(r[:80])
	}
	return text
}

// resolveHref resolves href against the document's URL, mirroring the
// browser's a.href resolution (the document URL stands in for baseURI; a
// <base> tag is deliberately not consulted). When either URL fails to parse
// the raw attribute value is returned unchanged.
func resolveHref(href, docURL string) string {
	base, err := url.Parse(docURL)
	if err != nil {
		return href
	}
	ref, err := url.Parse(href)
	if err != nil {
		return href
	}
	return base.ResolveReference(ref).String()
}

// nodeAttrMap parses the flattened name,value attribute pairs of element node
// i into a plain map. Unnamed or unvalued trailing pairs are ignored. Unlike
// the tool-layer nodeAttrs, id and class are kept in the map — candidacy and
// the inert filters read every attribute by name.
func nodeAttrMap(strs []string, attrs []proto.DOMSnapshotArrayOfStrings, i int) map[string]string {
	if i >= len(attrs) {
		return nil
	}
	pairs := attrs[i]
	m := make(map[string]string, len(pairs)/2)
	for k := 0; k+1 < len(pairs); k += 2 {
		name := str(strs, pairs[k])
		if name == "" {
			continue
		}
		m[name] = str(strs, pairs[k+1])
	}
	return m
}

// layoutIndex maps node-tree index → layout-tree entry for O(1) lookups.
func layoutIndex(layout *proto.DOMSnapshotLayoutTreeSnapshot) map[int]int {
	if layout == nil {
		return nil
	}
	m := make(map[int]int, len(layout.NodeIndex))
	for i, node := range layout.NodeIndex {
		m[node] = i
	}
	return m
}

// nodeTag resolves a node's tag name: the nodeName lowercased for elements
// ("DIV" → "div"), or the raw pseudo-name for non-elements ("#document",
// "#text", "#comment").
func nodeTag(strs []string, n *proto.DOMSnapshotNodeTreeSnapshot, i int) string {
	if i >= len(n.NodeName) {
		return ""
	}
	name := str(strs, n.NodeName[i])
	if name == "" || strings.HasPrefix(name, "#") {
		return name
	}
	return strings.ToLower(name)
}

// str resolves a string-table index, returning "" for negative or
// out-of-range indexes (a node may simply have no value).
func str(strs []string, idx proto.DOMSnapshotStringIndex) string {
	if idx < 0 || int(idx) >= len(strs) {
		return ""
	}
	return strs[idx]
}

// rareString returns the string-table value of the rare-data entry for node
// index i, or "" when the table has no entry for it. Rare data is small, so a
// linear scan is fine.
func rareString(strs []string, rd *proto.DOMSnapshotRareStringData, i int) string {
	if rd == nil {
		return ""
	}
	for k, node := range rd.Index {
		if node == i {
			return str(strs, rd.Value[k])
		}
	}
	return ""
}

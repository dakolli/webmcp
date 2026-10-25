package browsers

import (
	"context"
	"fmt"

	"github.com/go-rod/rod/lib/proto"
)

// MaxA11yNodes caps the emitted node list; A11yResult.Truncated reports the rest.
const MaxA11yNodes = 400

// maxA11yTextRunes caps one accessible name/value so a long value (e.g. a code
// editor's source) cannot dominate the response.
const maxA11yTextRunes = 200

// a11yStates is the curated, fixed-order state allowlist reported on each node:
// every other accessibility property (level, focusable, hidden, valuetext, …)
// is dropped to keep the a11y snapshot dense.
var a11yStates = []proto.AccessibilityAXPropertyName{
	proto.AccessibilityAXPropertyNameChecked,
	proto.AccessibilityAXPropertyNameExpanded,
	proto.AccessibilityAXPropertyNameSelected,
	proto.AccessibilityAXPropertyNameDisabled,
	proto.AccessibilityAXPropertyNameRequired,
	proto.AccessibilityAXPropertyNamePressed,
	proto.AccessibilityAXPropertyNameInvalid,
	proto.AccessibilityAXPropertyNameFocused,
}

// a11yNoiseRoles are the text-fragment roles skipped before the cap check: a11y
// reports semantics and actions, not prose — use what=text for prose.
var a11yNoiseRoles = map[string]bool{
	"StaticText":            true,
	"InlineTextBox":         true,
	"LabelText":             true,
	"paragraph":             true,
	"LineBreak":             true,
	"none":                  true,
	"generic":               true,
	"emphasis":              true,
	"strong":                true,
	"code":                  true,
	"Figcaption":            true,
	"DescriptionListDetail": true,
	"DescriptionListTerm":   true,
	"ListMarker":            true,
	"blockquote":            true,
	"mark":                  true,
	"Pre":                   true,
	"time":                  true,
	"subscript":             true,
	"superscript":           true,
}

// AXNode is one compact accessibility node in the flattened a11y snapshot.
type AXNode struct {
	// Handle is the g<document>-b<backendNodeId> handle minted for a
	// DOM-backed node, usable as page_act's handle arg. Omitted for nodes the
	// accessibility tree exposes without a DOM node.
	Handle string `json:"handle,omitempty"`
	// Depth is the node's tree depth: 0 for the root, from the ParentID chain.
	Depth int `json:"depth"`
	// Role is the computed semantic role, e.g. "button", "textbox".
	Role string `json:"role"`
	// Name is the accessible name, capped at 200 runes with a trailing ellipsis.
	Name string `json:"name,omitempty"`
	// Value is the current value, capped at 200 runes with a trailing ellipsis.
	Value string `json:"value,omitempty"`
	// States is the present state flags in fixed order, with "=mixed" appended
	// to the flags Chromium reports as mixed.
	States []string `json:"states,omitempty"`
}

// A11yResult is the wire shape of what=a11y. Its json tags ARE the envelope.
type A11yResult struct {
	// Nodes is the flattened tree in DFS order, never nil on the wire ([] on
	// an empty page).
	Nodes []AXNode `json:"nodes"`
	// Count is len(Nodes), never greater than MaxA11yNodes.
	Count int `json:"count"`
	// Truncated is the count of matching nodes dropped beyond the cap (0 when
	// none).
	Truncated int `json:"truncated"`
}

// A11yNodes returns the document's flattened accessibility tree in DFS order,
// with depth from the ParentID chain and semantics only (role/name/value/state);
// DOM-backed nodes carry a handle minted against the current document token.
//
// The method takes the page lock itself (mirroring Page.CaptureSnapshot):
// callers must NOT pre-lock, and the nil-Rod guard runs under the lock. The
// document-marker sequence is the same one CaptureSnapshot uses, so an a11y
// handle goes stale exactly like an interactives or dom handle after
// navigation. The tree is ONE Accessibility.getFullAXTree round trip covering
// the root frame only: iframe contents are separate documents.
func (p *Page) A11yNodes(ctx context.Context) (A11yResult, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return A11yResult{}, fmt.Errorf("page session is closed")
	}

	page := p.Rod.Context(ctx)
	if !p.docMarkerInstalled {
		if _, err := page.EvalOnNewDocument(docMarkerScript); err != nil {
			return A11yResult{}, fmt.Errorf("install document marker: %w", err)
		}
		p.docMarkerInstalled = true
	}
	token, err := documentToken(page)
	if err != nil {
		return A11yResult{}, err
	}
	res, err := proto.AccessibilityGetFullAXTree{}.Call(page)
	if err != nil {
		return A11yResult{}, fmt.Errorf("capture a11y tree: %w", err)
	}
	return flattenA11y(res.Nodes, token), nil
}

// flattenA11y turns the protocol's AX node list into the compact wire shape:
// ignored/roled-out/prose-only nodes are dropped, depth is resolved through
// ParentID (never array-index arithmetic), states are filtered to the curated
// allowlist, and handles are minted with docToken for DOM-backed nodes. It is a
// pure function over the protocol structs (no rod) so it is testable without
// Chromium.
func flattenA11y(nodes []*proto.AccessibilityAXNode, docToken string) A11yResult {
	res := A11yResult{Nodes: []AXNode{}}

	byID := make(map[proto.AccessibilityAXNodeID]*proto.AccessibilityAXNode, len(nodes))
	for _, n := range nodes {
		if n != nil {
			byID[n.NodeID] = n
		}
	}
	depthOf := make(map[proto.AccessibilityAXNodeID]int, len(nodes))
	visiting := make(map[proto.AccessibilityAXNodeID]bool, len(nodes))
	var depth func(id proto.AccessibilityAXNodeID) int
	depth = func(id proto.AccessibilityAXNodeID) int {
		if d, ok := depthOf[id]; ok {
			return d
		}
		n := byID[id]
		if n == nil || n.ParentID == "" || n.ParentID == id || visiting[id] {
			return 0
		}
		visiting[id] = true
		d := depth(n.ParentID) + 1
		delete(visiting, id)
		depthOf[id] = d
		return d
	}

	for _, n := range nodes {
		if n == nil || n.Ignored {
			continue
		}
		role := axValue(n.Role)
		if role == "" || a11yNoiseRoles[role] {
			continue
		}
		name := axValue(n.Name)
		if role == "image" && name == "" {
			continue
		}
		value := axValue(n.Value)
		if len(res.Nodes) >= MaxA11yNodes {
			res.Truncated++
			continue
		}
		node := AXNode{
			Depth:  depth(n.NodeID),
			Role:   role,
			Name:   clipA11yText(name),
			Value:  clipA11yText(value),
			States: axStates(n.Properties),
		}
		if n.BackendDOMNodeID != 0 {
			node.Handle = MintHandle(docToken, n.BackendDOMNodeID)
		}
		res.Nodes = append(res.Nodes, node)
	}
	res.Count = len(res.Nodes)
	return res
}

// clipA11yText caps s at maxA11yTextRunes runes on a rune boundary, appending
// an ellipsis when it cuts.
func clipA11yText(s string) string {
	r := []rune(s)
	if len(r) <= maxA11yTextRunes {
		return s
	}
	return string(r[:maxA11yTextRunes]) + "…"
}

// axValue reads one computed AX value, returning "" for a missing value or a
// JSON null (gson's Str() would render the null as "<nil>").
func axValue(v *proto.AccessibilityAXValue) string {
	if v == nil || v.Value.Nil() {
		return ""
	}
	return v.Value.Str()
}

// axStates returns the state flags in the fixed a11yStates order: a bare flag
// name when the property value is "true", "flag=mixed" when it is "mixed", and
// nothing for any other value (including invalid's "grammar"/"spelling").
func axStates(props []*proto.AccessibilityAXProperty) []string {
	if len(props) == 0 {
		return nil
	}
	present := make(map[proto.AccessibilityAXPropertyName]string, len(props))
	for _, p := range props {
		if p != nil {
			present[p.Name] = axValue(p.Value)
		}
	}
	var states []string
	for _, want := range a11yStates {
		switch present[want] {
		case "true":
			states = append(states, string(want))
		case "mixed":
			states = append(states, string(want)+"=mixed")
		}
	}
	return states
}

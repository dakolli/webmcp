package browsers

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// MaxInteractives caps how many interactive elements the root document
// returns. Each child frame has its own MaxFrameInteractives budget and all
// child frames together share MaxFrameInteractivesTotal, so a busy iframe can
// no longer consume the root document's budget.
const MaxInteractives = 200

// Frame budgets: per child frame and across all child frames.
const (
	// MaxFrameInteractives caps one child frame's entries.
	MaxFrameInteractives = 50
	// MaxFrameInteractivesTotal caps every child frame together.
	MaxFrameInteractivesTotal = 150
	// MaxInteractivesTotal is the hard ceiling for one response.
	MaxInteractivesTotal = MaxInteractives + MaxFrameInteractivesTotal
)

// InteractiveBox is an element's layout box in page-absolute CSS px: the
// top-left corner (X, Y) plus the size (W, H). It complements Interactive's
// centre X/Y so an agent can click an edge or reason about layout directly.
type InteractiveBox struct {
	// X is the box's left edge.
	X float64 `json:"x"`
	// Y is the box's top edge.
	Y float64 `json:"y"`
	// W is the box width.
	W float64 `json:"w"`
	// H is the box height.
	H float64 `json:"h"`
}

// Interactive is one clickable/typeable element in an interactives snapshot:
// the dense action-map entry an agent uses to act immediately via
// page_act { handle: ... } without selector guesswork.
type Interactive struct {
	// Handle is the element handle "g<document>-b<backendNodeId>" minted from
	// the snapshot's BackendNodeID parallel array. The document token scopes it
	// to the document that minted it, so a handle from a previous document is
	// rejected as stale; usable directly as page_act's handle arg. Omitted when
	// the snapshot carried no backend node id for the element.
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
	// Width is the width of the element's layout bounds in CSS px (0 for a
	// zero-size box), so an agent can size a target instead of only locating it.
	Width float64 `json:"width"`
	// Height is the height of the element's layout bounds in CSS px (0 for a
	// zero-size box).
	Height float64 `json:"height"`
	// Box is the same layout bounds as left/top plus width/height, so a caller
	// can act on an edge without recomputing it from the centre and size.
	// Present on every rendered element (a zero-size box reports 0s).
	Box *InteractiveBox `json:"box,omitempty"`
	// FrameURL is the URL of the child frame that owns this element, or empty
	// for the page's top-level document. Entries are emitted top document
	// first, then each child frame in snapshot order.
	FrameURL string `json:"frame_url,omitempty"`
	// Visible is always true: hidden/inert elements are skipped before the
	// snapshot is built, so every reported entry is rendered and interactable.
	Visible bool `json:"visible"`
}

// InteractiveFrame reports one child frame in an interactives snapshot: its
// document URL, how many entries that frame contributed, and how many of its
// candidates its budget cut.
type InteractiveFrame struct {
	// URL is the child document's URL ("about:blank" when it has none).
	URL string `json:"url"`
	// Count is the number of entries this frame contributed.
	Count int `json:"count"`
	// Truncated is this frame's remaining candidate count.
	Truncated int `json:"truncated"`
}

// InteractivesResult is the wire shape of an interactives snapshot: the ordered
// action map, the root document's remainder, and one row per child frame with
// its own remainder. Truncated and FramesTruncated are deliberately separate:
// folding them into one number loses where the cut fell.
type InteractivesResult struct {
	// Interactives is the page's interactive elements in document order (root
	// first, then each child frame), never null on the wire ([] on an empty
	// page).
	Interactives []Interactive `json:"interactives"`
	// Truncated is the ROOT document's remaining count only.
	Truncated int `json:"truncated"`
	// Frames has one row per child frame, never null on the wire ([] when the
	// page has no child frames).
	Frames []InteractiveFrame `json:"frames"`
	// FramesTruncated is the total remaining count across every child frame.
	FramesTruncated int `json:"frames_truncated"`
}

// interactiveScope narrows an interactives read to the subtree of one resolved
// element, identified by its renderer-global backend node id.
type interactiveScope struct {
	backendNodeID proto.DOMBackendNodeID
}

// Interactives returns the page's interactive elements as a dense action map:
// every visible, non-inert clickable/typeable element (anchors, buttons,
// inputs, selects, textareas, ARIA-role widgets, tabindex/onclick/
// contenteditable elements) with a WEB-51 handle, role, truncated text, href,
// input type and page-absolute center/box geometry — the snapshot an agent uses
// to act immediately (page_act { handle: ... }) without selector guesswork.
//
// Budgets are split: the root document returns up to MaxInteractives entries,
// each child frame up to MaxFrameInteractives, and every child frame together
// up to MaxFrameInteractivesTotal, so one response never exceeds
// MaxInteractivesTotal. Truncated is the root remainder only; FramesTruncated is
// the total across child frames; Frames reports each child frame.
//
// selector scopes the read to one element's subtree. It is resolved once in the
// page (page.Element -> DOM.describeNode backend node id) and then matched
// against the snapshot's BackendNodeID table, never by matching CSS in Go. A
// selector on an <iframe> returns that frame's entries with page-absolute
// coordinates; any other selector returns only root-document entries inside its
// subtree and no frame rows; a selector that matches nothing fails with the
// typed element-not-found error naming the selector. A scoped root subtree uses
// the root budget, a scoped frame uses the frame budget, and truncated reports
// the cut inside that scope.
//
// The method takes the page lock itself (mirroring Page.Close): callers must
// NOT pre-lock. The nil-Rod guard runs UNDER the lock and returns the exact
// pinned "page session is closed" error. One lock scope holds the marker read,
// the optional selector resolution and the ONE CaptureDOMSnapshot round trip,
// so the selector's backend node id and the snapshot always describe the same
// document. Child-frame bounds are frame-local in the snapshot, so the walk
// accumulates each frame's page-absolute origin through the owner nodes'
// ContentDocumentIndex links (parent origin + owner bounds - child scroll) and
// applies it before rounding. Backend node ids are renderer-global, so a frame
// handle minted with the page's top-level document token remains valid for
// page_act. Cross-origin out-of-process iframes are not part of the snapshot,
// so their elements are not listed. A frame whose document is missing or empty
// is skipped, never failing the whole read.
//
// No page DOM JS is evaluated: the snapshot's layout tree only contains
// rendered nodes, so the visibility filter is more accurate than an
// offsetParent heuristic (it does not wrongly exclude visible position:fixed
// elements). The walk applies the candidacy rules (tag/role/tabindex/onclick/
// contenteditable), skips inert elements (disabled, aria-disabled="true",
// aria-hidden="true" on the node or any ancestor) and unrendered nodes (no
// layout entry), and collapses nested interactives to the outermost. ctx is
// threaded through to the rod calls so a hung page cannot outlive the caller's
// deadline.
func (p *Page) Interactives(ctx context.Context, selector string) (InteractivesResult, error) {
	if err := p.lockRead(ctx, "interactives"); err != nil {
		return InteractivesResult{}, err
	}
	defer p.Unlock()
	if p.Rod == nil {
		return InteractivesResult{}, fmt.Errorf("page session is closed")
	}

	page := p.Rod.Context(ctx)
	if !p.docMarkerInstalled {
		if _, err := page.EvalOnNewDocument(docMarkerScript); err != nil {
			return InteractivesResult{}, fmt.Errorf("install document marker: %w", err)
		}
		p.docMarkerInstalled = true
	}
	token, err := documentToken(page)
	if err != nil {
		return InteractivesResult{}, err
	}
	var scope *interactiveScope
	if selector != "" {
		scope, err = resolveInteractiveScope(page, selector)
		if err != nil {
			return InteractivesResult{}, err
		}
	}
	snap, err := page.CaptureDOMSnapshot()
	if err != nil {
		return InteractivesResult{}, fmt.Errorf("capture DOM snapshot: %w", err)
	}
	res, err := decodeInteractivesScoped(snap, token, scope)
	if err != nil {
		return InteractivesResult{}, fmt.Errorf("decode interactives: %w", err)
	}
	return res, nil
}

// resolveInteractiveScope resolves selector once to the matched element's
// backend node id. It uses rod's no-retry sleeper, so a selector that matches
// nothing fails immediately with the typed *rod.ElementNotFoundError instead of
// burning the call budget.
func resolveInteractiveScope(page *rod.Page, selector string) (*interactiveScope, error) {
	el, err := page.Sleeper(rod.NotFoundSleeper).Element(selector)
	if err != nil {
		return nil, interactiveSelectorError(selector, err)
	}
	node, err := el.Describe(0, false)
	if err != nil {
		return nil, fmt.Errorf("resolve selector %q: describe node: %w", selector, err)
	}
	if node == nil || node.BackendNodeID == 0 {
		return nil, fmt.Errorf("resolve selector %q: node has no backend id", selector)
	}
	return &interactiveScope{backendNodeID: node.BackendNodeID}, nil
}

// interactiveSelectorError keeps a typed *rod.ElementNotFoundError in the wrap
// chain, so surfaces classify the failure as element_not_found while the
// message names the selector.
func interactiveSelectorError(selector string, err error) error {
	var missing *rod.ElementNotFoundError
	if errors.As(err, &missing) {
		return fmt.Errorf("no element matched selector %q: %w", selector, err)
	}
	return fmt.Errorf("resolve selector %q: %w", selector, err)
}

// decodeInteractives reads an unscoped snapshot. It is the pure entry point
// used by the snapshot-level tests.
func decodeInteractives(snap *proto.DOMSnapshotCaptureSnapshotResult, docToken string) (InteractivesResult, error) {
	return decodeInteractivesScoped(snap, docToken, nil)
}

// decodeInteractivesScoped walks every document in a DOM snapshot — the top
// document first, then each child frame in snapshot order — and collects the
// visible, non-inert interactives within their separate budgets. Each
// document's frame-local bounds are translated by the page-absolute origin
// accumulated from the frame-owner chain before rounding. scope, when non-nil,
// restricts the read to one resolved element's subtree or frame. It is a pure
// function over the snapshot structs (no rod), so it is unit-testable without
// Chromium: an empty-but-valid root document yields the empty result (never
// nil), while a snapshot with no root document at all is a decode failure. A
// child document that is missing or empty contributes only an empty frame row.
func decodeInteractivesScoped(
	snap *proto.DOMSnapshotCaptureSnapshotResult,
	docToken string,
	scope *interactiveScope,
) (InteractivesResult, error) {
	res := InteractivesResult{
		Interactives: []Interactive{},
		Frames:       []InteractiveFrame{},
	}
	if snap == nil || len(snap.Documents) == 0 || snap.Documents[0] == nil {
		return res, fmt.Errorf("snapshot has no root document")
	}
	root := snap.Documents[0]
	if root.Nodes == nil || len(root.Nodes.ParentIndex) == 0 {
		return res, nil
	}
	origins := frameOrigins(snap)
	if scope != nil {
		return decodeScopedInteractives(snap, docToken, scope, origins)
	}

	res.Truncated = collectDocumentInteractives(
		snap.Strings, root, docToken, "", origins[0], MaxInteractives, nil, &res.Interactives,
	)
	framesAdded := 0
	for i, doc := range snap.Documents[1:] {
		frameURL := frameDocumentURL(snap.Strings, doc)
		row := InteractiveFrame{URL: frameURL}
		if doc != nil && doc.Nodes != nil && len(doc.Nodes.ParentIndex) > 0 {
			limit := MaxFrameInteractives
			if remaining := MaxFrameInteractivesTotal - framesAdded; remaining < limit {
				limit = remaining
			}
			before := len(res.Interactives)
			row.Truncated = collectDocumentInteractives(
				snap.Strings, doc, docToken, frameURL, origins[i+1], limit, nil, &res.Interactives,
			)
			row.Count = len(res.Interactives) - before
			framesAdded += row.Count
		}
		res.Frames = append(res.Frames, row)
		res.FramesTruncated += row.Truncated
	}
	return res, nil
}

// decodeScopedInteractives resolves a selector scope against the snapshot. A
// scope on a frame owner returns that child document's entries with the frame
// budget; any other scope in the root document returns only entries inside its
// subtree with the root budget. A scope outside the root document (defensive;
// page.Element resolves in the top frame) is treated as a frame scope.
func decodeScopedInteractives(
	snap *proto.DOMSnapshotCaptureSnapshotResult,
	docToken string,
	scope *interactiveScope,
	origins []frameOrigin,
) (InteractivesResult, error) {
	docIdx, nodeIdx, ok := findBackendNode(snap, scope.backendNodeID)
	if !ok {
		return InteractivesResult{}, fmt.Errorf(
			"selector node %d is not in the DOM snapshot (the page changed during the read)",
			scope.backendNodeID,
		)
	}
	if child, linked := childDocumentOf(snap, docIdx, nodeIdx); linked {
		return frameScopedInteractives(snap, docToken, origins, child), nil
	}
	if docIdx == 0 && isFrameElement(snap.Strings, snap.Documents[docIdx], nodeIdx) {
		return frameScopedInteractives(snap, docToken, origins, 0), nil
	}
	if docIdx != 0 {
		return frameScopedInteractives(snap, docToken, origins, docIdx), nil
	}

	res := InteractivesResult{
		Interactives: []Interactive{},
		Frames:       []InteractiveFrame{},
	}
	root := snap.Documents[0]
	subtree := subtreeSet(root.Nodes.ParentIndex, nodeIdx)
	res.Truncated = collectDocumentInteractives(
		snap.Strings, root, docToken, "", origins[0], MaxInteractives, subtree, &res.Interactives,
	)
	return res, nil
}

// frameScopedInteractives returns one child document's entries under the
// per-frame budget. docIdx 0 means a frame owner without a snapshot document,
// which reports an empty about:blank row instead of failing.
func frameScopedInteractives(
	snap *proto.DOMSnapshotCaptureSnapshotResult,
	docToken string,
	origins []frameOrigin,
	docIdx int,
) InteractivesResult {
	res := InteractivesResult{
		Interactives: []Interactive{},
		Frames:       []InteractiveFrame{},
	}
	row := InteractiveFrame{URL: "about:blank"}
	if docIdx > 0 && docIdx < len(snap.Documents) {
		doc := snap.Documents[docIdx]
		row.URL = frameDocumentURL(snap.Strings, doc)
		if doc != nil && doc.Nodes != nil && len(doc.Nodes.ParentIndex) > 0 {
			row.Truncated = collectDocumentInteractives(
				snap.Strings, doc, docToken, row.URL, origins[docIdx], MaxFrameInteractives, nil, &res.Interactives,
			)
			row.Count = len(res.Interactives)
		}
	}
	res.Frames = append(res.Frames, row)
	res.FramesTruncated = row.Truncated
	return res
}

// findBackendNode returns the snapshot document and node index whose
// BackendNodeID matches id. Backend node ids are renderer-global, so at most
// one node matches.
func findBackendNode(
	snap *proto.DOMSnapshotCaptureSnapshotResult,
	id proto.DOMBackendNodeID,
) (int, int, bool) {
	for d, doc := range snap.Documents {
		if doc == nil || doc.Nodes == nil {
			continue
		}
		for i, backend := range doc.Nodes.BackendNodeID {
			if backend == id {
				return d, i, true
			}
		}
	}
	return 0, 0, false
}

// childDocumentOf returns the child document index linked to nodeIdx by the
// owning document's ContentDocumentIndex rare data.
func childDocumentOf(
	snap *proto.DOMSnapshotCaptureSnapshotResult,
	docIdx, nodeIdx int,
) (int, bool) {
	if docIdx < 0 || docIdx >= len(snap.Documents) {
		return 0, false
	}
	doc := snap.Documents[docIdx]
	if doc == nil || doc.Nodes == nil || doc.Nodes.ContentDocumentIndex == nil {
		return 0, false
	}
	owners := doc.Nodes.ContentDocumentIndex
	for k, owner := range owners.Index {
		if owner != nodeIdx || k >= len(owners.Value) {
			continue
		}
		child := owners.Value[k]
		if child > 0 && child < len(snap.Documents) {
			return child, true
		}
	}
	return 0, false
}

// isFrameElement reports whether nodeIdx names an iframe or frame element in
// doc, used only for a frame owner with no snapshot child document.
func isFrameElement(strs []string, doc *proto.DOMSnapshotDocumentSnapshot, nodeIdx int) bool {
	if doc == nil || doc.Nodes == nil {
		return false
	}
	switch nodeTag(strs, doc.Nodes, nodeIdx) {
	case "iframe", "frame":
		return true
	}
	return false
}

// subtreeSet marks nodeIdx and every descendant reachable by walking
// ParentIndex, so a scoped read can restrict the document walk without
// matching CSS in Go.
func subtreeSet(parents []int, nodeIdx int) map[int]bool {
	set := make(map[int]bool)
	if nodeIdx < 0 || nodeIdx >= len(parents) {
		return set
	}
	for i := range parents {
		for a := i; a >= 0 && a < len(parents); {
			if a == nodeIdx {
				set[i] = true
				break
			}
			parent := parents[a]
			if parent == a || parent < 0 || parent >= len(parents) {
				break
			}
			a = parent
		}
	}
	return set
}

// frameDocumentURL returns a child document's URL, mapping an absent document
// or an empty URL to about:blank.
func frameDocumentURL(strs []string, doc *proto.DOMSnapshotDocumentSnapshot) string {
	if doc == nil {
		return "about:blank"
	}
	url := str(strs, doc.DocumentURL)
	if url == "" {
		return "about:blank"
	}
	return url
}

// frameOrigin is a document's page-absolute origin: the offset added to that
// document's frame-local layout bounds to get page coordinates.
type frameOrigin struct {
	x, y float64
}

// frameOrigins maps each snapshot document index to its page-absolute origin by
// walking the ContentDocumentIndex links from the top document down. A child's
// origin is its parent's origin plus the owner iframe's layout bounds, minus the
// child document's own scroll offset. When an owner node has no layout entry the
// child keeps the parent origin instead of being dropped, so a frame chain
// deeper than one level still accumulates correctly. Documents not reachable
// from the top document keep the zero origin.
func frameOrigins(snap *proto.DOMSnapshotCaptureSnapshotResult) []frameOrigin {
	origins := make([]frameOrigin, len(snap.Documents))
	if len(origins) == 0 {
		return origins
	}
	assigned := make([]bool, len(origins))
	assigned[0] = true
	queue := []int{0}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		doc := snap.Documents[parent]
		if doc == nil || doc.Nodes == nil || doc.Nodes.ContentDocumentIndex == nil {
			continue
		}
		owners := doc.Nodes.ContentDocumentIndex
		for k, owner := range owners.Index {
			if k >= len(owners.Value) {
				break
			}
			child := owners.Value[k]
			if child <= 0 || child >= len(origins) || child == parent || assigned[child] {
				continue
			}
			origin := origins[parent]
			if b, ok := nodeBounds(doc, owner); ok {
				origin.x += b[0]
				origin.y += b[1]
			}
			if childDoc := snap.Documents[child]; childDoc != nil {
				if childDoc.ScrollOffsetX != nil {
					origin.x -= *childDoc.ScrollOffsetX
				}
				if childDoc.ScrollOffsetY != nil {
					origin.y -= *childDoc.ScrollOffsetY
				}
			}
			origins[child] = origin
			assigned[child] = true
			queue = append(queue, child)
		}
	}
	return origins
}

// nodeBounds returns node i's raw layout bounds in doc, or false when the node
// has no layout entry (an unrendered or detached frame owner).
func nodeBounds(doc *proto.DOMSnapshotDocumentSnapshot, i int) (proto.DOMSnapshotRectangle, bool) {
	if doc == nil || doc.Layout == nil {
		return nil, false
	}
	li, ok := layoutIndex(doc.Layout)[i]
	if !ok || li < 0 || li >= len(doc.Layout.Bounds) {
		return nil, false
	}
	b := doc.Layout.Bounds[li]
	if len(b) < 4 {
		return nil, false
	}
	return b, true
}

// collectDocumentInteractives appends up to limit of one document's visible,
// non-inert interactives to out in document order, and returns the number of
// remaining candidates that did not fit. origin is the document's page-absolute
// offset, already accumulated from its frame-owner chain. subtree, when non-nil,
// restricts the walk to those node indices (a selector scope). Only element
// nodes that satisfy the candidacy rules qualify; inert nodes (a disabled
// attribute or aria-disabled="true"), unrendered nodes (no layout entry) and
// nodes under an aria-hidden="true" ancestor are skipped, and a candidate nested
// inside an already-accepted interactive collapses to the outermost one.
// Attribute maps are parsed once so candidacy and the filters are pure lookups
// afterwards. frameURL is empty for the top document and the child frame's URL
// otherwise.
func collectDocumentInteractives(
	strs []string,
	doc *proto.DOMSnapshotDocumentSnapshot,
	docToken, frameURL string,
	origin frameOrigin,
	limit int,
	subtree map[int]bool,
	out *[]Interactive,
) int {
	n := doc.Nodes
	if n == nil || len(n.ParentIndex) == 0 {
		return 0
	}

	layout := layoutIndex(doc.Layout)

	attrsByNode := make([]map[string]string, len(n.ParentIndex))
	for i := range n.ParentIndex {
		if i < len(n.NodeType) && n.NodeType[i] == 1 {
			attrsByNode[i] = nodeAttrMap(strs, n.Attributes, i)
		}
	}

	accepted := make(map[int]bool, MaxInteractives)
	added := 0
	truncated := 0
	for i := range n.ParentIndex {
		if subtree != nil && !subtree[i] {
			continue
		}
		if i >= len(n.NodeType) || n.NodeType[i] != 1 {
			continue
		}
		tag := nodeTag(strs, n, i)
		attrs := attrsByNode[i]
		if !isInteractiveCandidate(tag, attrs) {
			continue
		}
		if _, has := attrs["disabled"]; has {
			continue
		}
		if strings.EqualFold(attrs["aria-disabled"], "true") {
			continue
		}
		li, hasLayout := layout[i]
		if !hasLayout {
			continue
		}
		if hiddenOrAncestorHidden(n.ParentIndex, attrsByNode, i) {
			continue
		}
		if ancestorAccepted(n.ParentIndex, i, accepted) {
			continue
		}
		if added >= limit {
			accepted[i] = true
			truncated++
			continue
		}
		accepted[i] = true
		*out = append(*out, buildInteractive(strs, doc, i, tag, attrs, li, docToken, frameURL, origin))
		added++
	}
	return truncated
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
// input type, the page-absolute center plus size of the layout bounds and the
// box's top-left corner. li is the node's layout-tree entry index (always valid
// for accepted candidates); origin is the document's accumulated frame offset,
// applied to the raw bounds before the single rounding pass. A zero-size box
// reports width/height 0 rather than omitting them, and the center is derived
// from the rounded box so x - width/2 reproduces box.x. frameURL is stamped on
// framed entries and left empty for the top document.
func buildInteractive(
	strs []string,
	doc *proto.DOMSnapshotDocumentSnapshot,
	i int,
	tag string,
	attrs map[string]string,
	li int,
	docToken, frameURL string,
	origin frameOrigin,
) Interactive {
	it := Interactive{
		Role:     roleOf(tag, attrs),
		Text:     textOf(strs, doc, i, li, attrs),
		FrameURL: frameURL,
		Visible:  true,
	}
	n := doc.Nodes
	if i < len(n.BackendNodeID) && n.BackendNodeID[i] > 0 {
		it.Handle = MintHandle(docToken, n.BackendNodeID[i])
	}
	if tag == "a" {
		it.Href = resolveHref(attrs["href"], str(strs, doc.DocumentURL))
	}
	if tag == "input" {
		it.Type = attrs["type"]
	}
	if li >= 0 && li < len(doc.Layout.Bounds) && len(doc.Layout.Bounds[li]) >= 4 {
		b := doc.Layout.Bounds[li]
		it.Box = &InteractiveBox{
			X: round10(b[0] + origin.x),
			Y: round10(b[1] + origin.y),
			W: round10(b[2]),
			H: round10(b[3]),
		}
		it.Width = it.Box.W
		it.Height = it.Box.H
		it.X = it.Box.X + it.Box.W/2
		it.Y = it.Box.Y + it.Box.H/2
	}
	return it
}

// round10 rounds v to one decimal place — the precision the action map reports
// coordinates and sizes at, matching the set-of-marks table.
func round10(v float64) float64 {
	return math.Round(v*10) / 10
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

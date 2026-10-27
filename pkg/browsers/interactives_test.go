package browsers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// TestDecodeInteractivesGeometry pins the action map's geometry contract: every
// accepted entry reports the centre and the size of its layout bounds at
// one-decimal precision, a zero-size box reports 0 instead of dropping the
// fields, and an element with no layout entry is still skipped.
func TestDecodeInteractivesGeometry(t *testing.T) {
	res, err := decodeInteractives(geometrySnapshot(), "test")
	if err != nil {
		t.Fatalf("decodeInteractives() error = %v, want nil", err)
	}
	want := []Interactive{
		{
			Handle:  "gtest-b101",
			Role:    "button",
			Text:    "Sign in",
			X:       60,
			Y:       40,
			Width:   100,
			Height:  40,
			Box:     &InteractiveBox{X: 10, Y: 20, W: 100, H: 40},
			Visible: true,
		},
		{
			Handle:  "gtest-b102",
			Role:    "button",
			Text:    "Add to cart",
			X:       56.25,
			Y:       44.75,
			Width:   88.5,
			Height:  21.5,
			Box:     &InteractiveBox{X: 12, Y: 34, W: 88.5, H: 21.5},
			Visible: true,
		},
		{
			Handle:  "gtest-b103",
			Role:    "button",
			Text:    "Zero",
			X:       5,
			Y:       5,
			Width:   0,
			Height:  0,
			Box:     &InteractiveBox{X: 5, Y: 5, W: 0, H: 0},
			Visible: true,
		},
	}
	if len(res.Interactives) != len(want) {
		t.Fatalf("decodeInteractives() returned %d interactives, want %d: %+v", len(res.Interactives), len(want), res.Interactives)
	}
	for i, w := range want {
		got := res.Interactives[i]
		if got.Box == nil {
			t.Fatalf("interactives[%d] box is nil, want %+v", i, *w.Box)
		}
		if *got.Box != *w.Box {
			t.Fatalf("interactives[%d] box = %+v, want %+v", i, *got.Box, *w.Box)
		}
		got.Box, w.Box = nil, nil
		if got != w {
			t.Fatalf("interactives[%d] = %+v, want %+v", i, got, w)
		}
	}
	if res.Truncated != 0 {
		t.Fatalf("decodeInteractives() truncated = %d, want 0", res.Truncated)
	}
	if len(res.Frames) != 0 || res.FramesTruncated != 0 {
		t.Fatalf("root-only decode frames = %+v frames_truncated = %d, want none", res.Frames, res.FramesTruncated)
	}
	envelope, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal interactives result: %v", err)
	}
	for _, field := range []string{`"frames":[]`, `"frames_truncated":0`} {
		if !strings.Contains(string(envelope), field) {
			t.Fatalf("root-only JSON = %s, want it to contain %s", envelope, field)
		}
	}

	b, err := json.Marshal(res.Interactives[2])
	if err != nil {
		t.Fatalf("marshal zero-size interactive: %v", err)
	}
	for _, field := range []string{`"width":0`, `"height":0`, `"box":{`} {
		if !strings.Contains(string(b), field) {
			t.Fatalf("zero-size interactive JSON = %s, want it to contain %s", b, field)
		}
	}
}

// TestInteractivesPayloadGrowth holds width/height to the ticket's payload
// budget and logs the cost at both ends of the entry spectrum: a full action
// map of labelled links must stay under 25 percent growth, while a synthetic
// map of bare entries (no text, no href) shows the absolute per-entry cost.
func TestInteractivesPayloadGrowth(t *testing.T) {
	tests := []struct {
		name string
		text string
		href string
	}{
		{
			name: "labelled links",
			text: strings.Repeat("label ", 13),
			href: "https://example.com/a/rather/long/path/that/still/resolves?query=value",
		},
		{name: "bare entries"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := make([]Interactive, MaxInteractives)
			legacy := make([]legacyInteractive, MaxInteractives)
			for i := range entries {
				handle := fmt.Sprintf("g3k9x2ab-b%d", 1000+i)
				entries[i] = Interactive{
					Handle:  handle,
					Role:    "a",
					Text:    tt.text,
					Href:    tt.href,
					X:       1234.5,
					Y:       5678.9,
					Width:   88.5,
					Height:  21.5,
					Visible: true,
				}
				legacy[i] = legacyInteractive{
					Handle:  handle,
					Role:    "a",
					Text:    tt.text,
					Href:    tt.href,
					X:       1234.5,
					Y:       5678.9,
					Visible: true,
				}
			}
			before, err := json.Marshal(legacy)
			if err != nil {
				t.Fatalf("marshal legacy interactives: %v", err)
			}
			after, err := json.Marshal(entries)
			if err != nil {
				t.Fatalf("marshal interactives: %v", err)
			}
			growth := 100 * float64(len(after)-len(before)) / float64(len(before))
			perEntry := (len(after) - len(before)) / MaxInteractives
			t.Logf("%d entries: %d B → %d B (+%.1f%%), +%d B per entry", MaxInteractives, len(before), len(after), growth, perEntry)
			if tt.text != "" && growth >= 25 {
				t.Fatalf("interactives payload grew %.1f%%, want < 25%%", growth)
			}
		})
	}
}

// legacyInteractive is the pre-WEB-116 action-map entry shape, kept only to
// measure what width/height add to the payload.
type legacyInteractive struct {
	Handle  string  `json:"handle"`
	Role    string  `json:"role"`
	Text    string  `json:"text,omitempty"`
	Href    string  `json:"href,omitempty"`
	Type    string  `json:"type,omitempty"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Visible bool    `json:"visible"`
}

// TestDecodeInteractivesFrames pins the iframe coverage contract: the top
// document keeps its entries first, each child frame's interactives are
// appended with frame_url, handles keep the g<document>-b<backendNodeId> form
// minted from the page's top-level token, and a frame with no document is
// skipped instead of failing the read.
func TestDecodeInteractivesFrames(t *testing.T) {
	res, err := decodeInteractives(twoFrameSnapshot(), "test")
	if err != nil {
		t.Fatalf("decodeInteractives() error = %v, want nil", err)
	}
	if len(res.Interactives) != 2 {
		t.Fatalf("decodeInteractives() returned %d interactives, want 2: %+v", len(res.Interactives), res.Interactives)
	}
	if res.Truncated != 0 {
		t.Fatalf("decodeInteractives() truncated = %d, want 0", res.Truncated)
	}
	if len(res.Frames) != 3 {
		t.Fatalf("frames = %+v, want one row per child document (3)", res.Frames)
	}
	if row := res.Frames[0]; row.URL != "https://example.com/frame" || row.Count != 1 || row.Truncated != 0 {
		t.Fatalf("frame row = %+v, want {https://example.com/frame 1 0}", row)
	}
	if res.FramesTruncated != 0 {
		t.Fatalf("frames_truncated = %d, want 0", res.FramesTruncated)
	}

	root := res.Interactives[0]
	if root.Handle != "gtest-b201" || root.FrameURL != "" {
		t.Fatalf("top entry = {handle:%q frame_url:%q}, want gtest-b201 with no frame_url", root.Handle, root.FrameURL)
	}
	if root.Box == nil || *root.Box != (InteractiveBox{X: 10, Y: 20, W: 100, H: 40}) {
		t.Fatalf("top entry box = %+v, want {10 20 100 40}", root.Box)
	}

	child := res.Interactives[1]
	if child.Handle != "gtest-b301" {
		t.Fatalf("frame entry handle = %q, want gtest-b301 (same top-level document token)", child.Handle)
	}
	if child.FrameURL != "https://example.com/frame" {
		t.Fatalf("frame entry frame_url = %q, want https://example.com/frame", child.FrameURL)
	}
	if child.Href != "https://example.com/child" {
		t.Fatalf("frame entry href = %q, want it resolved against the frame URL", child.Href)
	}
	if child.Box == nil || *child.Box != (InteractiveBox{X: 30, Y: 40, W: 60, H: 20}) {
		t.Fatalf("frame entry box = %+v, want {30 40 60 20}", child.Box)
	}
}

// TestDecodeInteractivesFrameOverflow pins the split budgets: a small root
// keeps all its entries, the busy frame is cut at its 50-entry budget, and the
// root and frame remainders are reported separately.
func TestDecodeInteractivesFrameOverflow(t *testing.T) {
	res, err := decodeInteractives(budgetSnapshot(3, 201), "test")
	if err != nil {
		t.Fatalf("decodeInteractives() error = %v, want nil", err)
	}
	if want := 3 + MaxFrameInteractives; len(res.Interactives) != want {
		t.Fatalf("decodeInteractives() returned %d interactives, want %d", len(res.Interactives), want)
	}
	if res.Truncated != 0 {
		t.Fatalf("root truncated = %d, want 0 (the root had 3 candidates)", res.Truncated)
	}
	if len(res.Frames) != 1 {
		t.Fatalf("frames = %+v, want 1 row", res.Frames)
	}
	row := res.Frames[0]
	if row.Count != MaxFrameInteractives || row.Truncated != 201-MaxFrameInteractives {
		t.Fatalf("frame row = %+v, want count %d truncated %d", row, MaxFrameInteractives, 201-MaxFrameInteractives)
	}
	if res.FramesTruncated != 201-MaxFrameInteractives {
		t.Fatalf("frames_truncated = %d, want %d", res.FramesTruncated, 201-MaxFrameInteractives)
	}
	if res.Interactives[0].FrameURL != "" || res.Interactives[2].FrameURL != "" {
		t.Fatal("root entries must not carry frame_url")
	}
	if res.Interactives[3].FrameURL != "https://example.com/frame" {
		t.Fatalf("entry 3 frame_url = %q, want the child frame URL", res.Interactives[3].FrameURL)
	}
	if res.Interactives[3].Handle != "gtest-b2002" {
		t.Fatalf("first frame entry handle = %q, want gtest-b2002", res.Interactives[3].Handle)
	}
}

// TestDecodeInteractivesFrameBudget pins one frame's own budget: 60 candidates
// return 50 entries and a truncated row count of 10.
func TestDecodeInteractivesFrameBudget(t *testing.T) {
	res, err := decodeInteractives(budgetSnapshot(0, 60), "test")
	if err != nil {
		t.Fatalf("decodeInteractives() error = %v, want nil", err)
	}
	if len(res.Interactives) != MaxFrameInteractives {
		t.Fatalf("returned %d interactives, want %d", len(res.Interactives), MaxFrameInteractives)
	}
	if res.Truncated != 0 {
		t.Fatalf("root truncated = %d, want 0", res.Truncated)
	}
	if len(res.Frames) != 1 {
		t.Fatalf("frames = %+v, want 1 row", res.Frames)
	}
	row := res.Frames[0]
	if row.Count != MaxFrameInteractives || row.Truncated != 10 {
		t.Fatalf("frame row = %+v, want {count:%d truncated:10}", row, MaxFrameInteractives)
	}
	if res.FramesTruncated != 10 {
		t.Fatalf("frames_truncated = %d, want 10", res.FramesTruncated)
	}
}

// TestDecodeInteractivesFrameTotalBudget pins the 150-entry aggregate across
// frames: four frames of 60 candidates contribute 50, 50, 50 then 0, and the
// fourth frame's 60 candidates are all reported as its remainder.
func TestDecodeInteractivesFrameTotalBudget(t *testing.T) {
	res, err := decodeInteractives(budgetSnapshot(0, 60, 60, 60, 60), "test")
	if err != nil {
		t.Fatalf("decodeInteractives() error = %v, want nil", err)
	}
	if len(res.Interactives) != MaxFrameInteractivesTotal {
		t.Fatalf("returned %d interactives, want the %d-entry frame aggregate", len(res.Interactives), MaxFrameInteractivesTotal)
	}
	if len(res.Frames) != 4 {
		t.Fatalf("frames = %+v, want 4 rows", res.Frames)
	}
	counts := make([]int, len(res.Frames))
	for i, row := range res.Frames {
		counts[i] = row.Count
	}
	wantCounts := []int{50, 50, 50, 0}
	for i := range wantCounts {
		if counts[i] != wantCounts[i] {
			t.Fatalf("frame %d count = %d, want %d (all counts %v)", i, counts[i], wantCounts[i], counts)
		}
	}
	if res.FramesTruncated != 90 {
		t.Fatalf("frames_truncated = %d, want 90 (10+10+10+60)", res.FramesTruncated)
	}
}

// TestDecodeInteractivesSelectorSubtree pins selector scoping: a scope on a
// root container returns only the interactives inside its subtree and no frame
// rows.
func TestDecodeInteractivesSelectorSubtree(t *testing.T) {
	res, err := decodeInteractivesScoped(subtreeSnapshot(), "test", &interactiveScope{backendNodeID: 500})
	if err != nil {
		t.Fatalf("decodeInteractivesScoped() error = %v, want nil", err)
	}
	if len(res.Interactives) != 2 {
		t.Fatalf("returned %d interactives, want the 2 inside the subtree: %+v", len(res.Interactives), res.Interactives)
	}
	for _, entry := range res.Interactives {
		if entry.Text != "Inside one" && entry.Text != "Inside two" {
			t.Fatalf("scoped entry = %+v, want only subtree entries", entry)
		}
	}
	if res.Truncated != 0 || res.FramesTruncated != 0 {
		t.Fatalf("truncated = %d frames_truncated = %d, want 0", res.Truncated, res.FramesTruncated)
	}
	if len(res.Frames) != 0 {
		t.Fatalf("frames = %+v, want none for a root subtree scope", res.Frames)
	}
}

// TestDecodeInteractivesSelectorIframe pins selector scoping on a frame owner:
// the scope returns that child frame's entries with page-absolute coordinates
// and one frame row, using the frame budget.
func TestDecodeInteractivesSelectorIframe(t *testing.T) {
	res, err := decodeInteractivesScoped(offsetSnapshot(), "test", &interactiveScope{backendNodeID: 111})
	if err != nil {
		t.Fatalf("decodeInteractivesScoped() error = %v, want nil", err)
	}
	if len(res.Interactives) != 1 {
		t.Fatalf("returned %d interactives, want the child frame's 1 entry", len(res.Interactives))
	}
	got := res.Interactives[0]
	if got.Box == nil || *got.Box != (InteractiveBox{X: 404, Y: 138, W: 19.6, H: 17}) {
		t.Fatalf("scoped frame box = %+v, want page-absolute {404 138 19.6 17}", got.Box)
	}
	if got.FrameURL != "https://example.com/frame" {
		t.Fatalf("scoped frame_url = %q, want the child frame URL", got.FrameURL)
	}
	if len(res.Frames) != 1 || res.Frames[0].Count != 1 || res.Frames[0].URL != "https://example.com/frame" {
		t.Fatalf("frames = %+v, want one row for the scoped frame", res.Frames)
	}
	if res.Truncated != 0 || res.FramesTruncated != 0 {
		t.Fatalf("truncated = %d frames_truncated = %d, want 0", res.Truncated, res.FramesTruncated)
	}
}

// TestInteractiveSelectorNotFound pins the no-match contract: the typed
// element-not-found error stays in the wrap chain and the message names the
// selector, so the surface reports error.type=element_not_found.
func TestInteractiveSelectorNotFound(t *testing.T) {
	const selector = "#does-not-exist"
	err := interactiveSelectorError(selector, &rod.ElementNotFoundError{})
	if err == nil {
		t.Fatal("interactiveSelectorError() = nil, want an error")
	}
	if !strings.Contains(err.Error(), selector) {
		t.Fatalf("error %q does not name the selector %q", err, selector)
	}
	var missing *rod.ElementNotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("error %q does not wrap *rod.ElementNotFoundError", err)
	}
}

// TestInteractivesSelectorScopes exercises the real selector path
// (page.Element -> DOM.describeNode) against a local page: a container selector
// returns only its subtree, and an iframe selector returns that frame's entries
// with page-absolute coordinates.
func TestInteractivesSelectorScopes(t *testing.T) {
	p := newCanaryTestPage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/child" {
			_, _ = fmt.Fprint(w, `<a id="inner" href="/inner">Inner</a>`)
			return
		}
		_, _ = fmt.Fprint(w, `<div id="scope"><a id="scoped" href="/scoped">Scoped</a></div>`+
			`<a id="outside" href="/outside">Outside</a>`+
			`<iframe id="frame" src="/child" style="position:absolute;left:400px;top:200px;width:300px;height:200px;border:0"></iframe>`)
	}))
	t.Cleanup(server.Close)

	if err := p.Rod.Context(ctx).Navigate(server.URL); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if err := p.Rod.Context(ctx).WaitLoad(); err != nil {
		t.Fatalf("wait load: %v", err)
	}

	t.Run("container subtree", func(t *testing.T) {
		res, err := p.Interactives(ctx, "#scope")
		if err != nil {
			t.Fatalf("Interactives(#scope): %v", err)
		}
		if len(res.Interactives) != 1 || !strings.HasSuffix(res.Interactives[0].Href, "/scoped") {
			t.Fatalf("scoped entries = %+v, want only the /scoped link", res.Interactives)
		}
		if len(res.Frames) != 0 {
			t.Fatalf("frames = %+v, want none for a root subtree scope", res.Frames)
		}
	})

	t.Run("iframe", func(t *testing.T) {
		res, err := p.Interactives(ctx, "#frame")
		if err != nil {
			t.Fatalf("Interactives(#frame): %v", err)
		}
		if len(res.Interactives) != 1 || !strings.HasSuffix(res.Interactives[0].Href, "/inner") {
			t.Fatalf("iframe entries = %+v, want only the /inner link", res.Interactives)
		}
		if len(res.Frames) != 1 || res.Frames[0].Count != 1 {
			t.Fatalf("frames = %+v, want one row with count 1", res.Frames)
		}
		if res.Interactives[0].Box == nil || res.Interactives[0].Box.X < 300 {
			t.Fatalf("iframe entry box = %+v, want page-absolute x >= 300", res.Interactives[0].Box)
		}
	})

	t.Run("no match", func(t *testing.T) {
		const selector = "#does-not-exist"
		_, err := p.Interactives(ctx, selector)
		if err == nil {
			t.Fatal("Interactives(#does-not-exist) = nil error, want element-not-found")
		}
		if !strings.Contains(err.Error(), selector) {
			t.Fatalf("error %q does not name the selector %q", err, selector)
		}
		var missing *rod.ElementNotFoundError
		if !errors.As(err, &missing) {
			t.Fatalf("error %q does not wrap *rod.ElementNotFoundError", err)
		}
	})
}

// TestDecodeInteractivesFrameOffset pins page-absolute coordinates for a
// framed entry: a frame-local box is translated by the owner iframe's layout
// position before rounding, so click coordinates land where the element is on
// the page. The numbers mirror the live runoob defect.
func TestDecodeInteractivesFrameOffset(t *testing.T) {
	res, err := decodeInteractives(offsetSnapshot(), "test")
	if err != nil {
		t.Fatalf("decodeInteractives() error = %v, want nil", err)
	}
	if len(res.Interactives) != 1 {
		t.Fatalf("decodeInteractives() returned %d interactives, want 1: %+v", len(res.Interactives), res.Interactives)
	}
	got := res.Interactives[0]
	want := InteractiveBox{X: 404, Y: 138, W: 19.6, H: 17}
	if got.Box == nil || *got.Box != want {
		t.Fatalf("framed box = %+v, want %+v", got.Box, want)
	}
	if got.X != 413.8 || got.Y != 146.5 {
		t.Fatalf("framed centre = (%v,%v), want (413.8,146.5)", got.X, got.Y)
	}
	if got.Width != 19.6 || got.Height != 17 {
		t.Fatalf("framed size = (%v,%v), want (19.6,17)", got.Width, got.Height)
	}
	if round10(got.X-got.Width/2) != got.Box.X || round10(got.Y-got.Height/2) != got.Box.Y {
		t.Fatalf("centre - half size = (%v,%v), want box origin (%v,%v)",
			got.X-got.Width/2, got.Y-got.Height/2, got.Box.X, got.Box.Y)
	}
	if got.FrameURL != "https://example.com/frame" {
		t.Fatalf("framed frame_url = %q, want https://example.com/frame", got.FrameURL)
	}
}

// TestDecodeInteractivesNestedFrameOffset pins offset accumulation through two
// frame levels and scroll subtraction at each level: a grandchild entry must
// carry the sum of both owner positions minus both documents' scroll offsets.
func TestDecodeInteractivesNestedFrameOffset(t *testing.T) {
	res, err := decodeInteractives(nestedOffsetSnapshot(), "test")
	if err != nil {
		t.Fatalf("decodeInteractives() error = %v, want nil", err)
	}
	if len(res.Interactives) != 1 {
		t.Fatalf("decodeInteractives() returned %d interactives, want 1: %+v", len(res.Interactives), res.Interactives)
	}
	got := res.Interactives[0]
	want := InteractiveBox{X: 116, Y: 54, W: 10, H: 10}
	if got.Box == nil || *got.Box != want {
		t.Fatalf("nested framed box = %+v, want %+v", got.Box, want)
	}
	if got.X != 121 || got.Y != 59 {
		t.Fatalf("nested framed centre = (%v,%v), want (121,59)", got.X, got.Y)
	}
	if got.FrameURL != "https://example.com/nested" {
		t.Fatalf("nested frame_url = %q, want https://example.com/nested", got.FrameURL)
	}
}

// TestDecodeInteractivesBlankFrameURL pins the about:blank fallback: a child
// document with no URL must not inherit the parent's URL or omit the field.
func TestDecodeInteractivesBlankFrameURL(t *testing.T) {
	res, err := decodeInteractives(blankURLSnapshot(), "test")
	if err != nil {
		t.Fatalf("decodeInteractives() error = %v, want nil", err)
	}
	if len(res.Interactives) != 1 {
		t.Fatalf("decodeInteractives() returned %d interactives, want 1: %+v", len(res.Interactives), res.Interactives)
	}
	if got := res.Interactives[0].FrameURL; got != "about:blank" {
		t.Fatalf("blank frame_url = %q, want about:blank", got)
	}
}

const (
	testDocumentName proto.DOMSnapshotStringIndex = 0
	testHTMLName     proto.DOMSnapshotStringIndex = 1
	testIframeName   proto.DOMSnapshotStringIndex = 2
	testAnchorName   proto.DOMSnapshotStringIndex = 3
	testHrefName     proto.DOMSnapshotStringIndex = 4
	testHrefValue    proto.DOMSnapshotStringIndex = 5
	testRootURL      proto.DOMSnapshotStringIndex = 6
	testChildURL     proto.DOMSnapshotStringIndex = 7
	testNestedURL    proto.DOMSnapshotStringIndex = 8
	testChildText    proto.DOMSnapshotStringIndex = 9
)

// frameStrings is the shared string table for the frame-offset snapshots.
func frameStrings() []string {
	return []string{
		"#document", "html", "iframe", "a", "href", "/child",
		"https://example.com/",
		"https://example.com/frame",
		"https://example.com/nested",
		"Child",
	}
}

// testNode is one element in a testDocument.
type testNode struct {
	tag     proto.DOMSnapshotStringIndex
	backend proto.DOMBackendNodeID
	attrs   []proto.DOMSnapshotStringIndex
	text    proto.DOMSnapshotStringIndex
	bounds  proto.DOMSnapshotRectangle
}

// testDocument builds a document whose first two nodes are the #document root
// and <html>, followed by elements parented to <html>. A non-negative
// childOwner/childDoc pair links the frame-owner node to a child document.
func testDocument(
	url proto.DOMSnapshotStringIndex,
	scrollX, scrollY *float64,
	childOwner, childDoc int,
	elements ...testNode,
) *proto.DOMSnapshotDocumentSnapshot {
	size := 2 + len(elements)
	parents := make([]int, size)
	types := make([]int, size)
	names := make([]proto.DOMSnapshotStringIndex, size)
	backends := make([]proto.DOMBackendNodeID, size)
	attrs := make([]proto.DOMSnapshotArrayOfStrings, size)
	bounds := make([]proto.DOMSnapshotRectangle, size)
	texts := make([]proto.DOMSnapshotStringIndex, size)
	nodeIdx := make([]int, size)
	parents[0], parents[1] = -1, 0
	types[0], types[1] = 9, 1
	names[0], names[1] = testDocumentName, testHTMLName
	for i := 0; i < size; i++ {
		nodeIdx[i] = i
		texts[i] = -1
	}
	bounds[0] = proto.DOMSnapshotRectangle{0, 0, 800, 600}
	bounds[1] = proto.DOMSnapshotRectangle{0, 0, 800, 600}
	for j, el := range elements {
		i := j + 2
		parents[i] = 1
		types[i] = 1
		names[i] = el.tag
		backends[i] = el.backend
		attrs[i] = el.attrs
		texts[i] = el.text
		bounds[i] = el.bounds
	}
	doc := &proto.DOMSnapshotDocumentSnapshot{
		DocumentURL: url,
		Nodes: &proto.DOMSnapshotNodeTreeSnapshot{
			ParentIndex:   parents,
			NodeType:      types,
			NodeName:      names,
			BackendNodeID: backends,
			Attributes:    attrs,
		},
		Layout: &proto.DOMSnapshotLayoutTreeSnapshot{
			NodeIndex: nodeIdx,
			Bounds:    bounds,
			Text:      texts,
		},
		ScrollOffsetX: scrollX,
		ScrollOffsetY: scrollY,
	}
	if childOwner >= 0 && childDoc >= 0 {
		doc.Nodes.ContentDocumentIndex = &proto.DOMSnapshotRareIntegerData{
			Index: []int{childOwner},
			Value: []int{childDoc},
		}
	}
	return doc
}

// offsetSnapshot is a top document with an iframe at (396,130) whose child
// document holds one anchor at frame-local (8,8).
func offsetSnapshot() *proto.DOMSnapshotCaptureSnapshotResult {
	return &proto.DOMSnapshotCaptureSnapshotResult{
		Strings: frameStrings(),
		Documents: []*proto.DOMSnapshotDocumentSnapshot{
			testDocument(testRootURL, nil, nil, 2, 1,
				testNode{
					tag:     testIframeName,
					backend: 111,
					bounds:  proto.DOMSnapshotRectangle{396, 130, 400, 300},
				},
			),
			testDocument(testChildURL, nil, nil, -1, -1,
				testNode{
					tag:     testAnchorName,
					backend: 301,
					attrs:   []proto.DOMSnapshotStringIndex{testHrefName, testHrefValue},
					text:    testChildText,
					bounds:  proto.DOMSnapshotRectangle{8, 8, 19.6, 17},
				},
			),
		},
	}
}

// nestedOffsetSnapshot chains two iframes with per-document scroll offsets:
// root iframe at (100,50) scrolls the child by (5,7), and the child's iframe at
// (20,10) scrolls the grandchild by (1,2), so the grandchild origin is (114,51).
func nestedOffsetSnapshot() *proto.DOMSnapshotCaptureSnapshotResult {
	scrollOneX, scrollOneY := 5.0, 7.0
	scrollTwoX, scrollTwoY := 1.0, 2.0
	return &proto.DOMSnapshotCaptureSnapshotResult{
		Strings: frameStrings(),
		Documents: []*proto.DOMSnapshotDocumentSnapshot{
			testDocument(testRootURL, nil, nil, 2, 1,
				testNode{
					tag:     testIframeName,
					backend: 111,
					bounds:  proto.DOMSnapshotRectangle{100, 50, 600, 400},
				},
			),
			testDocument(testChildURL, &scrollOneX, &scrollOneY, 2, 2,
				testNode{
					tag:     testIframeName,
					backend: 222,
					bounds:  proto.DOMSnapshotRectangle{20, 10, 300, 200},
				},
			),
			testDocument(testNestedURL, &scrollTwoX, &scrollTwoY, -1, -1,
				testNode{
					tag:     testAnchorName,
					backend: 333,
					attrs:   []proto.DOMSnapshotStringIndex{testHrefName, testHrefValue},
					text:    testChildText,
					bounds:  proto.DOMSnapshotRectangle{2, 3, 10, 10},
				},
			),
		},
	}
}

// blankURLSnapshot is a top document with an iframe whose child document has no
// URL string (−1 resolves to empty).
func blankURLSnapshot() *proto.DOMSnapshotCaptureSnapshotResult {
	return &proto.DOMSnapshotCaptureSnapshotResult{
		Strings: frameStrings(),
		Documents: []*proto.DOMSnapshotDocumentSnapshot{
			testDocument(testRootURL, nil, nil, 2, 1,
				testNode{
					tag:     testIframeName,
					backend: 111,
					bounds:  proto.DOMSnapshotRectangle{0, 0, 300, 200},
				},
			),
			testDocument(-1, nil, nil, -1, -1,
				testNode{
					tag:     testAnchorName,
					backend: 444,
					attrs:   []proto.DOMSnapshotStringIndex{testHrefName, testHrefValue},
					text:    testChildText,
					bounds:  proto.DOMSnapshotRectangle{5, 5, 10, 10},
				},
			),
		},
	}
}

// twoFrameSnapshot builds a top document with one button, a child frame with
// one anchor, then a nil document and a document with no node tree — both must
// be skipped rather than failing the decode.
func twoFrameSnapshot() *proto.DOMSnapshotCaptureSnapshotResult {
	const (
		documentName = 0
		htmlName     = 1
		buttonName   = 2
		anchorName   = 3
		rootURL      = 4
		rootText     = 5
		childURL     = 6
		childText    = 7
		hrefName     = 8
		hrefValue    = 9
	)
	strs := []string{
		"#document", "html", "button", "a",
		"https://example.com/", "Root",
		"https://example.com/frame", "Child",
		"href", "/child",
	}
	childAttrs := make([]proto.DOMSnapshotArrayOfStrings, 3)
	childAttrs[2] = []proto.DOMSnapshotStringIndex{hrefName, hrefValue}
	return &proto.DOMSnapshotCaptureSnapshotResult{
		Strings: strs,
		Documents: []*proto.DOMSnapshotDocumentSnapshot{
			{
				DocumentURL: rootURL,
				Nodes: &proto.DOMSnapshotNodeTreeSnapshot{
					ParentIndex:   []int{-1, 0, 1},
					NodeType:      []int{9, 1, 1},
					NodeName:      []proto.DOMSnapshotStringIndex{documentName, htmlName, buttonName},
					BackendNodeID: []proto.DOMBackendNodeID{0, 0, 201},
					Attributes:    make([]proto.DOMSnapshotArrayOfStrings, 3),
				},
				Layout: &proto.DOMSnapshotLayoutTreeSnapshot{
					NodeIndex: []int{0, 1, 2},
					Bounds: []proto.DOMSnapshotRectangle{
						{0, 0, 800, 600},
						{0, 0, 800, 600},
						{10, 20, 100, 40},
					},
					Text: []proto.DOMSnapshotStringIndex{-1, -1, rootText},
				},
			},
			{
				DocumentURL: childURL,
				Nodes: &proto.DOMSnapshotNodeTreeSnapshot{
					ParentIndex:   []int{-1, 0, 1},
					NodeType:      []int{9, 1, 1},
					NodeName:      []proto.DOMSnapshotStringIndex{documentName, htmlName, anchorName},
					BackendNodeID: []proto.DOMBackendNodeID{0, 0, 301},
					Attributes:    childAttrs,
				},
				Layout: &proto.DOMSnapshotLayoutTreeSnapshot{
					NodeIndex: []int{0, 1, 2},
					Bounds: []proto.DOMSnapshotRectangle{
						{0, 0, 400, 300},
						{0, 0, 400, 300},
						{30, 40, 60, 20},
					},
					Text: []proto.DOMSnapshotStringIndex{-1, -1, childText},
				},
			},
			nil,
			{},
		},
	}
}

// budgetSnapshot builds a top document with rootCount buttons and one child
// frame per frameCounts entry, all rendered, so the split budgets can be
// exercised without nesting.
func budgetSnapshot(rootCount int, frameCounts ...int) *proto.DOMSnapshotCaptureSnapshotResult {
	const (
		documentName = 0
		htmlName     = 1
		buttonName   = 2
		rootURL      = 3
		rootText     = 4
		childURL     = 5
		childText    = 6
	)
	strs := []string{
		"#document", "html", "button",
		"https://example.com/", "Root",
		"https://example.com/frame", "Child",
	}
	build := func(
		count, backendBase int,
		urlIdx, textIdx proto.DOMSnapshotStringIndex,
	) *proto.DOMSnapshotDocumentSnapshot {
		size := count + 2
		parents := make([]int, size)
		types := make([]int, size)
		names := make([]proto.DOMSnapshotStringIndex, size)
		backends := make([]proto.DOMBackendNodeID, size)
		nodeIdx := make([]int, size)
		bounds := make([]proto.DOMSnapshotRectangle, size)
		text := make([]proto.DOMSnapshotStringIndex, size)
		parents[0], parents[1] = -1, 0
		types[0], types[1] = 9, 1
		names[0], names[1] = documentName, htmlName
		for i := 0; i < size; i++ {
			nodeIdx[i] = i
			bounds[i] = proto.DOMSnapshotRectangle{0, 0, 800, 600}
			text[i] = -1
			if i > 1 {
				parents[i] = 1
				types[i] = 1
				names[i] = buttonName
				backends[i] = proto.DOMBackendNodeID(backendBase + i)
				text[i] = textIdx
			}
		}
		return &proto.DOMSnapshotDocumentSnapshot{
			DocumentURL: urlIdx,
			Nodes: &proto.DOMSnapshotNodeTreeSnapshot{
				ParentIndex:   parents,
				NodeType:      types,
				NodeName:      names,
				BackendNodeID: backends,
				Attributes:    make([]proto.DOMSnapshotArrayOfStrings, size),
			},
			Layout: &proto.DOMSnapshotLayoutTreeSnapshot{
				NodeIndex: nodeIdx,
				Bounds:    bounds,
				Text:      text,
			},
		}
	}
	docs := make([]*proto.DOMSnapshotDocumentSnapshot, 0, 1+len(frameCounts))
	docs = append(docs, build(rootCount, 1000, rootURL, rootText))
	for i, count := range frameCounts {
		docs = append(docs, build(count, 2000*(i+1), childURL, childText))
	}
	return &proto.DOMSnapshotCaptureSnapshotResult{
		Strings:   strs,
		Documents: docs,
	}
}

// subtreeSnapshot builds one root document with a #scope div containing two
// buttons and a sibling button outside it.
func subtreeSnapshot() *proto.DOMSnapshotCaptureSnapshotResult {
	const (
		documentName = 0
		htmlName     = 1
		divName      = 2
		buttonName   = 3
		documentURL  = 4
		insideOne    = 5
		insideTwo    = 6
		outside      = 7
		idName       = 8
		scopeID      = 9
	)
	strs := []string{
		"#document", "html", "div", "button",
		"https://example.com/", "Inside one", "Inside two", "Outside",
		"id", "scope",
	}
	attrs := make([]proto.DOMSnapshotArrayOfStrings, 6)
	attrs[2] = []proto.DOMSnapshotStringIndex{idName, scopeID}
	return &proto.DOMSnapshotCaptureSnapshotResult{
		Strings: strs,
		Documents: []*proto.DOMSnapshotDocumentSnapshot{{
			DocumentURL: documentURL,
			Nodes: &proto.DOMSnapshotNodeTreeSnapshot{
				ParentIndex:   []int{-1, 0, 1, 2, 2, 1},
				NodeType:      []int{9, 1, 1, 1, 1, 1},
				NodeName:      []proto.DOMSnapshotStringIndex{documentName, htmlName, divName, buttonName, buttonName, buttonName},
				BackendNodeID: []proto.DOMBackendNodeID{0, 0, 500, 501, 502, 503},
				Attributes:    attrs,
			},
			Layout: &proto.DOMSnapshotLayoutTreeSnapshot{
				NodeIndex: []int{0, 1, 2, 3, 4, 5},
				Bounds: []proto.DOMSnapshotRectangle{
					{0, 0, 800, 600},
					{0, 0, 800, 600},
					{10, 10, 400, 200},
					{20, 20, 100, 30},
					{20, 60, 100, 30},
					{500, 20, 100, 30},
				},
				Text: []proto.DOMSnapshotStringIndex{-1, -1, -1, insideOne, insideTwo, outside},
			},
		}},
	}
}

// geometrySnapshot builds a DOM snapshot with three rendered buttons — an
// integer box, a fractional box and a zero-size box — plus an unrendered link
// that the visibility filter must drop.
func geometrySnapshot() *proto.DOMSnapshotCaptureSnapshotResult {
	const (
		documentName  = 0
		htmlName      = 1
		buttonName    = 2
		anchorName    = 3
		documentURL   = 4
		signInText    = 5
		addToCartText = 6
		zeroText      = 7
		hrefName      = 8
		hrefValue     = 9
	)
	strs := []string{
		"#document",
		"html",
		"button",
		"a",
		"https://example.com/",
		"Sign in",
		"Add to cart",
		"Zero",
		"href",
		"/next",
	}
	attrs := make([]proto.DOMSnapshotArrayOfStrings, 6)
	attrs[5] = []proto.DOMSnapshotStringIndex{hrefName, hrefValue}
	return &proto.DOMSnapshotCaptureSnapshotResult{
		Strings: strs,
		Documents: []*proto.DOMSnapshotDocumentSnapshot{{
			DocumentURL: documentURL,
			Nodes: &proto.DOMSnapshotNodeTreeSnapshot{
				ParentIndex:   []int{-1, 0, 1, 1, 1, 1},
				NodeType:      []int{9, 1, 1, 1, 1, 1},
				NodeName:      []proto.DOMSnapshotStringIndex{documentName, htmlName, buttonName, buttonName, buttonName, anchorName},
				BackendNodeID: []proto.DOMBackendNodeID{0, 0, 101, 102, 103, 104},
				Attributes:    attrs,
			},
			Layout: &proto.DOMSnapshotLayoutTreeSnapshot{
				NodeIndex: []int{0, 1, 2, 3, 4},
				Bounds: []proto.DOMSnapshotRectangle{
					{0, 0, 800, 600},
					{0, 0, 800, 600},
					{10, 20, 100, 40},
					{12, 34, 88.5, 21.5},
					{5, 5, 0, 0},
				},
				Text: []proto.DOMSnapshotStringIndex{-1, -1, signInText, addToCartText, zeroText},
			},
		}},
	}
}

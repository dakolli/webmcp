package browsers

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

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
			Visible: true,
		},
		{
			Handle:  "gtest-b102",
			Role:    "button",
			Text:    "Add to cart",
			X:       56.3,
			Y:       44.8,
			Width:   88.5,
			Height:  21.5,
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
			Visible: true,
		},
	}
	if len(res.Interactives) != len(want) {
		t.Fatalf("decodeInteractives() returned %d interactives, want %d: %+v", len(res.Interactives), len(want), res.Interactives)
	}
	for i, w := range want {
		if res.Interactives[i] != w {
			t.Fatalf("interactives[%d] = %+v, want %+v", i, res.Interactives[i], w)
		}
	}
	if res.Truncated != 0 {
		t.Fatalf("decodeInteractives() truncated = %d, want 0", res.Truncated)
	}

	b, err := json.Marshal(res.Interactives[2])
	if err != nil {
		t.Fatalf("marshal zero-size interactive: %v", err)
	}
	for _, field := range []string{`"width":0`, `"height":0`} {
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

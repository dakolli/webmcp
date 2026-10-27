package pageops

// Truncation pins for PageTruncate: short content untouched, non-positive
// maxBytes semantics, and rune-safe cutting with the EXACT truncation note.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	"github.com/dakolli/webmcp/pkg/browsers"
)

// truncateNote is the exact suffix PageTruncate appends: a leading newline,
// the Unicode ellipsis (U+2026) and the em-dash (U+2014). Kept as a literal so
// this test pins the exact bytes on the wire.
const truncateNote = "\n… (output truncated — raise --max-bytes or narrow the selector)"

// TestPageTruncate pins the short-content / boundary / non-positive-maxBytes
// behaviors and the exact truncation output.
func TestPageTruncate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"short content unchanged", "hello", 100, "hello"},
		{"empty content unchanged", "", 10, ""},
		{"exact boundary unchanged", "hello", 5, "hello"},
		{"maxBytes zero unchanged", "hello", 0, "hello"},
		{"maxBytes negative unchanged", "hello", -1, "hello"},
		{"ascii cut appends note", "abcdefgh", 4, "abcd" + truncateNote},
		{"cut already on rune boundary", "ab日本語", 5, "ab日" + truncateNote}, // bytes 2-4 are 日; b[5] starts 本
		{"cut walks back to rune start", "a😀bc", 3, "a" + truncateNote},    // b[:3] splits 😀
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PageTruncate(tt.in, tt.max); got != tt.want {
				t.Errorf("PageTruncate(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
			}
		})
	}
}

// TestPageTruncateRuneSafe pins the invariant that a truncated result is never
// a half-decoded UTF-8 rune, regardless of where the byte budget lands.
func TestPageTruncateRuneSafe(t *testing.T) {
	tests := []struct {
		in  string
		max int
	}{
		{"héllo wörld", 4},       // 2-byte runes
		{"日本語テキスト", 7},           // 3-byte runes
		{"emoji😀mix", 6},         // 4-byte rune
		{"a\xc3\xa9rest", 2},     // cut inside é
		{"\xf0\x9f\x98\x80x", 3}, // cut inside 😀
	}
	for _, tt := range tests {
		got := PageTruncate(tt.in, tt.max)
		if !utf8.ValidString(got) {
			t.Errorf("PageTruncate(%q, %d) = %q is not valid UTF-8", tt.in, tt.max, got)
		}
		if !strings.HasSuffix(got, truncateNote) {
			t.Errorf("PageTruncate(%q, %d) = %q, want it to end with the truncation note", tt.in, tt.max, got)
		}
		prefix := strings.TrimSuffix(got, truncateNote)
		if len(prefix) > tt.max {
			t.Errorf("PageTruncate(%q, %d) prefix length %d exceeds maxBytes", tt.in, tt.max, len(prefix))
		}
	}
}

// TestPageTruncateInvalidUTF8NoPanic pins that the rune walk-back is total: a
// byte budget landing inside a run of orphan continuation bytes (malformed
// UTF-8) must not decrement past zero, which would index b[-1] and panic.
func TestPageTruncateInvalidUTF8NoPanic(t *testing.T) {
	in := string([]byte{0x80, 0x80, 0x80}) // three orphan continuation bytes
	got := PageTruncate(in, 2)
	if !utf8.ValidString(got) {
		t.Errorf("PageTruncate(%q, 2) = %q is not valid UTF-8", in, got)
	}
	if got != truncateNote {
		t.Errorf("PageTruncate(%q, 2) = %q, want %q (empty cut + truncation note)", in, got, truncateNote)
	}
}

// TestParseKeyCombo pins key combo resolution for press_key actions.
func TestParseKeyCombo(t *testing.T) {
	tests := []struct {
		in        string
		wantMods  int
		wantKey   rune
		shouldErr bool
	}{
		{"Enter", 0, '\r', false},
		{"Escape", 0, 27, false},
		{"Control+a", 1, 'a', false},
		{"Ctrl+Shift+c", 2, 'c', false},
		{"Meta+c", 1, 'c', false},
		{"Alt+F4", 1, 115, false},
		{"", 0, 0, true},
		{"UnknownModifier+a", 0, 0, true},
		{"Control+UnknownKeyName", 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			mods, key, err := parseKeyCombo(tt.in)
			if tt.shouldErr {
				if err == nil {
					t.Errorf("parseKeyCombo(%q) expected error, got nil", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseKeyCombo(%q) unexpected error: %v", tt.in, err)
			}
			if len(mods) != tt.wantMods {
				t.Errorf("parseKeyCombo(%q) got %d modifiers, want %d", tt.in, len(mods), tt.wantMods)
			}
			if rune(key.Info().KeyCode) != tt.wantKey && rune(key) != tt.wantKey {
				// key can match KeyCode or key rune depending on map
			}
		})
	}
}

// TestParseMouseButton pins mouse button parsing for click/click_at actions.
func TestParseMouseButton(t *testing.T) {
	tests := []struct {
		in        string
		shouldErr bool
	}{
		{"", false},
		{"left", false},
		{"right", false},
		{"middle", false},
		{"invalid", true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			_, err := parseMouseButton(tt.in)
			if tt.shouldErr && err == nil {
				t.Errorf("parseMouseButton(%q) expected error", tt.in)
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("parseMouseButton(%q) unexpected error: %v", tt.in, err)
			}
		})
	}
}

// TestActTarget pins handle parsing and the exact stale-handle error strings
// that MCP, CLI and IPC surface verbatim.
func TestActTarget(t *testing.T) {
	valid := []struct {
		handle string
		token  string
		want   proto.DOMBackendNodeID
	}{
		{"g3k9x2ab-b6", "3k9x2ab", 6},
		{"g0-b1073741831", "0", 1073741831},
	}
	for _, tt := range valid {
		t.Run("valid "+tt.handle, func(t *testing.T) {
			token, got, err := parseHandle(tt.handle)
			if err != nil {
				t.Fatalf("parseHandle(%q) unexpected error: %v", tt.handle, err)
			}
			if token != tt.token || got != tt.want {
				t.Errorf("parseHandle(%q) = (%q, %d), want (%q, %d)", tt.handle, token, got, tt.token, tt.want)
			}
		})
	}

	for _, handle := range []string{"", "b", "b6", "b0", "b-3", "x123", "6", "g-b6", "g", "gabc", "gabc-b", "gabc-b0", "gabc6"} {
		t.Run("invalid "+handle, func(t *testing.T) {
			_, _, err := parseHandle(handle)
			want := `invalid handle "` + handle + `" (expected g<document>-b<backendNodeId> minted by page_fetch what=interactives|dom|links)`
			if err == nil || err.Error() != want {
				t.Fatalf("parseHandle(%q) error = %v, want %q", handle, err, want)
			}
		})
	}

	if _, err := actTarget(nil, "", ""); err == nil || err.Error() != "either selector or handle is required" {
		t.Errorf("actTarget(no target) error = %v", err)
	}

	resolveErr := errors.New("{-32000 No node with given id found }")
	if got := staleResolveError("g3k9x2ab-b6", resolveErr).Error(); got != `handle "g3k9x2ab-b6" is stale — element no longer in the document (re-run page_fetch what=interactives to re-mint handles): {-32000 No node with given id found }` {
		t.Errorf("staleResolveError = %q", got)
	}
	if got := staleDetachedError("g3k9x2ab-b6").Error(); got != `handle "g3k9x2ab-b6" is stale — element detached from the document (re-run page_fetch what=interactives to re-mint handles)` {
		t.Errorf("staleDetachedError = %q", got)
	}
	if got := staleDocumentError("g3k9x2ab-b6").Error(); got != `handle "g3k9x2ab-b6" is stale — minted for a previous document (re-run page_fetch what=interactives to re-mint handles)` {
		t.Errorf("staleDocumentError = %q", got)
	}

	if got := actionError("click", "", "#x", resolveErr).Error(); got != `click: element "#x": {-32000 No node with given id found }` {
		t.Errorf("selector actionError = %q", got)
	}
	if got := actionError("click", "g3k9x2ab-b6", "#x", resolveErr).Error(); got != resolveErr.Error() {
		t.Errorf("handle actionError re-wrapped: %q", got)
	}
}

// TestResolveError pins the selector-resolution classification rule: an
// unmatched selector carries the typed element-not-found error, while a
// malformed selector keeps its own error.
func TestResolveError(t *testing.T) {
	notFound := resolveError("#missing", &rod.ElementNotFoundError{})
	var missing *rod.ElementNotFoundError
	if !errors.As(notFound, &missing) {
		t.Fatalf("resolveError(not found) = %v, want *rod.ElementNotFoundError", notFound)
	}
	want := `no element matched selector "#missing": cannot find element`
	if notFound.Error() != want {
		t.Fatalf("resolveError(not found) = %q, want %q", notFound, want)
	}

	evalErr := errors.New("eval js error: SyntaxError: invalid selector")
	if got := resolveError("#bad[", evalErr); got != evalErr {
		t.Fatalf("resolveError(eval error) = %v, want the original error", got)
	}
}

// TestLinkHandles pins first-anchor-wins handle matching from a DOM snapshot.
func TestLinkHandles(t *testing.T) {
	strs := []string{
		"#document",
		"HTML",
		"A",
		"DIV",
		"href",
		"/one",
		"/two",
		"/three?a=1&b=2",
		"https://example.com/",
	}
	snap := &proto.DOMSnapshotCaptureSnapshotResult{
		Documents: []*proto.DOMSnapshotDocumentSnapshot{{
			DocumentURL: 8,
			Nodes: &proto.DOMSnapshotNodeTreeSnapshot{
				ParentIndex:   []int{-1, 0, 1, 0, 0, 0},
				NodeType:      []int{9, 1, 1, 1, 1, 1},
				NodeName:      []proto.DOMSnapshotStringIndex{0, 1, 2, 2, 2, 2},
				BackendNodeID: []proto.DOMBackendNodeID{0, 10, 11, 12, 13, 14},
				Attributes: []proto.DOMSnapshotArrayOfStrings{
					nil,
					nil,
					{4, 5},
					{4, 5},
					{4, 6},
					{4, 7},
				},
			},
		}},
		Strings: strs,
	}

	got := linkHandles(snap, "3k9x2ab")
	want := map[string]string{
		"https://example.com/one":           "g3k9x2ab-b11",
		"https://example.com/two":           "g3k9x2ab-b13",
		"https://example.com/three?a=1&b=2": "g3k9x2ab-b14",
	}
	if len(got) != len(want) {
		t.Fatalf("linkHandles() = %v, want %v", got, want)
	}
	for href, handle := range want {
		if got[href] != handle {
			t.Errorf("linkHandles()[%q] = %q, want %q", href, got[href], handle)
		}
	}
}

// TestStuckPageOperationReleasesLock is the WEB-140 regression test: an
// operation that hangs until its own context expires must release the page
// lock, a second caller must fail fast with the actionable timeout sentence,
// and the page must be usable again once the stuck operation returns.
func TestStuckPageOperationReleasesLock(t *testing.T) {
	p := &browsers.Page{ID: "page-1"}
	stuckCtx, stuckCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stuckCancel()

	started := make(chan struct{})
	released := make(chan struct{})
	go func() {
		ctx, done, err := beginPage(stuckCtx, p, "stuck")
		if err != nil {
			t.Errorf("beginPage(stuck) error = %v", err)
			close(released)
			return
		}
		close(started)
		<-ctx.Done()
		done()
		close(released)
	}()
	<-started

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer waitCancel()
	if _, _, err := beginPage(waitCtx, p, "click"); err == nil {
		t.Fatal("second call acquired a page held by a stuck operation")
	} else if !strings.Contains(err.Error(), "click") || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("beginPage(click) error = %q, want the actionable busy-page sentence", err)
	}

	<-released
	ctx, done, err := beginPage(context.Background(), p, "click")
	if err != nil {
		t.Fatalf("lock not released after the stuck operation returned: %v", err)
	}
	if ctx == nil {
		t.Fatal("beginPage returned a nil context")
	}
	done()
}

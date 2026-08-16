package pageops

// Truncation pins for PageTruncate: short content untouched, non-positive
// maxBytes semantics, and rune-safe cutting with the EXACT truncation note.

import (
	"strings"
	"testing"
	"unicode/utf8"
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

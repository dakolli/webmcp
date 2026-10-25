package pageops

import "testing"

// TestInlineStyleEntries pins the inline <style> listing shape: 1-based
// inline-style[n] URLs, the inline-style type, and the body byte length.
func TestInlineStyleEntries(t *testing.T) {
	got := inlineStyleEntries([]string{"a{}", "bb{}"})
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[0].URL != "inline-style[1]" || got[0].Type != "inline-style" {
		t.Errorf("entry 0 = {%q, %q}, want {inline-style[1], inline-style}", got[0].URL, got[0].Type)
	}
	if got[0].Size == nil || *got[0].Size != 3 {
		t.Errorf("entry 0 size = %v, want 3", got[0].Size)
	}
	if got[1].URL != "inline-style[2]" || got[1].Size == nil || *got[1].Size != 4 {
		t.Errorf("entry 1 = {%q, %v}, want {inline-style[2], 4}", got[1].URL, got[1].Size)
	}
	if empty := inlineStyleEntries(nil); len(empty) != 0 {
		t.Errorf("inlineStyleEntries(nil) = %d entries, want 0", len(empty))
	}
}

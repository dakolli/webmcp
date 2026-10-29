package browsers

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ysmood/gson"
)

func TestClipMutationEntry(t *testing.T) {
	long := strings.Repeat("é", 200)
	got := clipMutationEntry(MutationEntry{Target: long, Text: long})
	if n := len([]rune(got.Target)); n != maxMutationTargetRunes+1 {
		t.Fatalf("target runes = %d, want %d", n, maxMutationTargetRunes+1)
	}
	if !strings.HasSuffix(got.Target, ellipsis) {
		t.Fatal("clipped target has no ellipsis")
	}
	if n := len([]rune(got.Text)); n != maxMutationTextRunes+1 {
		t.Fatalf("text runes = %d, want %d", n, maxMutationTextRunes+1)
	}
	if !strings.HasSuffix(got.Text, ellipsis) {
		t.Fatal("clipped text has no ellipsis")
	}

	short := clipMutationEntry(MutationEntry{Target: "#app", Text: "ok"})
	if short.Target != "#app" || short.Text != "ok" {
		t.Fatalf("short entry = %+v, want it unchanged", short)
	}
}

func TestMutationsSinceAndSelector(t *testing.T) {
	page := &Page{observer: newPageObserver()}
	page.observer.recordMutation(MutationEntry{Kind: "childList", Target: "div#app > ul", Added: 2})
	page.observer.recordMutation(MutationEntry{Kind: "characterData", Target: "div#app > p", Text: "hi"})
	page.observer.recordMutation(MutationEntry{Kind: "attributes", Target: "body > span", Attribute: "class"})

	t.Run("since keeps only newer entries", func(t *testing.T) {
		got, dropped, last := page.Mutations(1, "")
		if len(got) != 2 || dropped != 0 || last != 3 {
			t.Fatalf("since=1: entries=%d dropped=%d last=%d, want 2/0/3", len(got), dropped, last)
		}
		if got[0].Seq != 2 || got[1].Seq != 3 {
			t.Fatalf("seqs = %d,%d, want 2,3", got[0].Seq, got[1].Seq)
		}
	})

	t.Run("since at the newest returns nothing", func(t *testing.T) {
		got, _, last := page.Mutations(3, "")
		if len(got) != 0 || last != 3 {
			t.Fatalf("since=3: entries=%d last=%d, want 0/3", len(got), last)
		}
	})

	t.Run("selector filters by target containment", func(t *testing.T) {
		got, _, _ := page.Mutations(0, "#app")
		if len(got) != 2 {
			t.Fatalf("selector #app: entries=%d, want 2", len(got))
		}
		for _, e := range got {
			if !strings.Contains(e.Target, "#app") {
				t.Fatalf("selector leak: %q", e.Target)
			}
		}
	})
}

func TestDecodeMutation(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want MutationEntry
	}{
		{
			name: "childList batch",
			raw:  `{"kind":"childList","target":"div#app > ul","added":3,"removed":1,"ts":1700000000000}`,
			want: MutationEntry{
				Kind:    "childList",
				Target:  "div#app > ul",
				Added:   3,
				Removed: 1,
				TS:      1700000000000,
			},
		},
		{
			name: "attributes batch",
			raw:  `{"kind":"attributes","target":"body > span","attribute":"class","added":0,"removed":0,"ts":1700000000001}`,
			want: MutationEntry{
				Kind:      "attributes",
				Target:    "body > span",
				Attribute: "class",
				TS:        1700000000001,
			},
		},
		{
			name: "missing kind records unknown with raw length",
			raw:  `{"target":"#app","added":2}`,
			want: MutationEntry{
				Kind:   "unknown",
				Target: "#app",
				Text:   fmt.Sprintf("%d bytes", len(`{"target":"#app","added":2}`)),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeMutation(gson.NewFrom(tc.raw))
			if got != tc.want {
				t.Fatalf("decodeMutation(%s) = %+v, want %+v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestMutationRingBound(t *testing.T) {
	page := &Page{observer: newPageObserver()}
	total := MutationRingCap + 5
	for i := range total {
		page.observer.recordMutation(MutationEntry{
			Kind:   "childList",
			Target: fmt.Sprintf("div:nth-of-type(%d)", i),
		})
	}

	got, dropped, last := page.Mutations(0, "")
	if len(got) != MutationRingCap {
		t.Fatalf("ring entries = %d, want %d", len(got), MutationRingCap)
	}
	if dropped != 5 {
		t.Fatalf("dropped = %d, want 5", dropped)
	}
	if last != int64(total) {
		t.Fatalf("last_seq = %d, want %d", last, total)
	}
	if got[0].Seq != 6 {
		t.Fatalf("oldest retained seq = %d, want 6", got[0].Seq)
	}
}

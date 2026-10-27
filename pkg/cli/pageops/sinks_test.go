package pageops

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dakolli/webmcp/pkg/browsers"
)

// TestSinksAspectRegistered pins the registry entry: sinks validates, is
// self-capped so the envelope owns its bounds, and is named by the shared
// unknown-what error.
func TestSinksAspectRegistered(t *testing.T) {
	if err := ValidateAspect("sinks"); err != nil {
		t.Fatalf("ValidateAspect(sinks) = %v, want nil", err)
	}

	selfCapped := false
	for _, a := range aspectEntries {
		if a.name == "sinks" {
			selfCapped = a.selfCapped
		}
	}
	if !selfCapped {
		t.Fatal("sinks entry is not self-capped")
	}

	err := ValidateAspect("not-a-sink")
	if err == nil || !strings.Contains(err.Error(), "sinks") {
		t.Fatalf("unknown-what error = %v, want it to list sinks", err)
	}
}

// TestSinksPayloads pins the exact JSON the wrappers emit. PageSinks marshals
// the engine result unchanged, and install/clear marshal the same structs the
// wrappers use, so these bytes are the wire shape rather than a parallel model.
func TestSinksPayloads(t *testing.T) {
	t.Run("unarmed page keeps hits as an empty array", func(t *testing.T) {
		got, err := json.Marshal(browsers.SinksResult{Hits: []browsers.SinkHit{}})
		if err != nil {
			t.Fatalf("marshal unarmed: %v", err)
		}
		const want = `{"installed":false,"hits":[],"dropped":0,"truncated":0}`
		if string(got) != want {
			t.Fatalf("unarmed payload = %s, want %s", got, want)
		}
	})

	t.Run("one hit", func(t *testing.T) {
		res := browsers.SinksResult{
			Installed: true,
			Hits: []browsers.SinkHit{{
				Source:   "location.hash",
				Sink:     "innerHTML",
				Context:  "html",
				Value:    "<b>wm-canary</b>",
				Selector: "body",
				TS:       1750000000000,
			}},
		}
		got, err := json.Marshal(res)
		if err != nil {
			t.Fatalf("marshal armed: %v", err)
		}
		const want = `{"installed":true,"hits":[{"source":"location.hash","sink":"innerHTML","context":"html","value":"\u003cb\u003ewm-canary\u003c/b\u003e","selector":"body","ts":1750000000000}],"dropped":0,"truncated":0}`
		if string(got) != want {
			t.Fatalf("armed payload = %s, want %s", got, want)
		}
	})

	t.Run("install and clear envelopes", func(t *testing.T) {
		tests := []struct {
			name string
			got  []byte
			want string
		}{
			{"install", mustMarshal(t, sinksInstalled{Installed: true}), `{"installed":true}`},
			{"clear", mustMarshal(t, sinksCleared{Cleared: 3}), `{"cleared":3}`},
			{"clear unarmed", mustMarshal(t, sinksCleared{}), `{"cleared":0}`},
		}
		for _, tt := range tests {
			if string(tt.got) != tt.want {
				t.Errorf("%s payload = %s, want %s", tt.name, tt.got, tt.want)
			}
		}
	})
}

// mustMarshal encodes v and fails the test on error.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return b
}

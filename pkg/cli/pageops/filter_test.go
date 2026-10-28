package pageops

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dakolli/webmcp/pkg/browsers"
)

func TestNetworkFilterKeepsAndDrops(t *testing.T) {
	entries := []browsers.NetworkEntry{
		{Type: "Document"},
		{Type: "Script"},
		{Type: "Image"},
		{Type: "Font"},
		{Type: "XHR"},
	}
	cases := []struct {
		name        string
		filter      string
		wantTypes   []string
		wantDropped int
	}{
		{"bare names keep only those", "document,script", []string{"Document", "Script"}, 3},
		{"dash names drop only those", "-image,-font", []string{"Document", "Script", "XHR"}, 2},
		{"matching is case-insensitive", "DOCUMENT,xhr", []string{"Document", "XHR"}, 3},
		{"empty tokens are ignored", "script,", []string{"Script"}, 4},
		{"empty filter keeps everything", "", []string{"Document", "Script", "Image", "Font", "XHR"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parseNetworkFilter(tc.filter)
			if err != nil {
				t.Fatalf("parseNetworkFilter(%q) error = %v", tc.filter, err)
			}
			kept, dropped := f.apply(entries)
			if got := len(kept); got != len(tc.wantTypes) {
				t.Fatalf("kept %d entries, want %d", got, len(tc.wantTypes))
			}
			for i, want := range tc.wantTypes {
				if kept[i].Type != want {
					t.Errorf("kept[%d].Type = %q, want %q", i, kept[i].Type, want)
				}
			}
			if dropped != tc.wantDropped {
				t.Errorf("dropped = %d, want %d", dropped, tc.wantDropped)
			}
		})
	}
}

func TestSinksFilterCanaryAndKnown(t *testing.T) {
	hits := []browsers.SinkHit{
		{Source: "unknown", Sink: "innerHTML", Value: "<b>page content</b>"},
		{Source: "location.hash", Sink: "innerHTML", Value: "#wm-canary-1a2b3c4d"},
		{Source: "unknown", Sink: "eval", Value: "wm-canary-ffffffff"},
	}
	cases := []struct {
		name     string
		filter   string
		want     int
		dropped  int
		wantSink []string
	}{
		{"canary keeps values holding the marker", "canary", 2, 1, []string{"innerHTML", "eval"}},
		{"known drops unknown source", "known", 1, 2, []string{"innerHTML"}},
		{"empty filter keeps every hit", "", 3, 0, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parseSinksFilter(tc.filter)
			if err != nil {
				t.Fatalf("parseSinksFilter(%q) error = %v", tc.filter, err)
			}
			kept, dropped := f.apply(hits)
			if len(kept) != tc.want || dropped != tc.dropped {
				t.Fatalf("kept %d hits and dropped %d, want %d and %d", len(kept), dropped, tc.want, tc.dropped)
			}
			for i, sink := range tc.wantSink {
				if kept[i].Sink != sink {
					t.Errorf("kept[%d].Sink = %q, want %q", i, kept[i].Sink, sink)
				}
			}
		})
	}
}

func TestFilterRejectsUnknownValues(t *testing.T) {
	_, err := parseNetworkFilter("script,bogus")
	if err == nil || !strings.Contains(err.Error(), "accepted:") {
		t.Fatalf("parseNetworkFilter(\"script,bogus\") error = %v, want one listing the accepted types", err)
	}
	if _, err := parseSinksFilter("everything"); err == nil {
		t.Fatal("parseSinksFilter(\"everything\") error = nil, want an invalid filter error")
	}
}

func TestFilteredEnvelopesKeepUnfilteredShape(t *testing.T) {
	network, err := encodeLogs([]browsers.NetworkEntry{{Type: "Document"}}, 0, "network")
	if err != nil {
		t.Fatalf("encodeLogs() error = %v", err)
	}
	if strings.Contains(network, "filtered_out") {
		t.Errorf("unfiltered network envelope carries filtered_out: %s", network)
	}
	if !strings.Contains(network, `"count":1`) {
		t.Errorf("unfiltered network envelope = %s, want count 1", network)
	}
	filtered, err := encodeFilteredLogs([]browsers.NetworkEntry{{Type: "Document"}}, 0, 3, "network")
	if err != nil {
		t.Fatalf("encodeFilteredLogs() error = %v", err)
	}
	if !strings.Contains(filtered, `"filtered_out":3`) {
		t.Errorf("filtered network envelope = %s, want filtered_out 3", filtered)
	}

	plain, err := json.Marshal(browsers.SinksResult{Installed: true, Hits: []browsers.SinkHit{}})
	if err != nil {
		t.Fatalf("marshal SinksResult: %v", err)
	}
	unfiltered, err := json.Marshal(sinksEnvelope{
		SinksResult: browsers.SinksResult{Installed: true, Hits: []browsers.SinkHit{}},
	})
	if err != nil {
		t.Fatalf("marshal sinksEnvelope: %v", err)
	}
	if string(unfiltered) != string(plain) {
		t.Errorf("unfiltered sinks envelope = %s, want %s", unfiltered, plain)
	}
	zero := 0
	filteredSinks, err := json.Marshal(sinksEnvelope{
		SinksResult: browsers.SinksResult{Installed: true, Hits: []browsers.SinkHit{}},
		FilteredOut: &zero,
	})
	if err != nil {
		t.Fatalf("marshal filtered sinksEnvelope: %v", err)
	}
	if !strings.Contains(string(filteredSinks), `"filtered_out":0`) {
		t.Errorf("filtered sinks envelope = %s, want filtered_out 0", filteredSinks)
	}
}

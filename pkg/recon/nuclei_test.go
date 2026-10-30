package recon

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNucleiArgvDefaults(t *testing.T) {
	t.Parallel()

	req := NucleiRequest{Targets: []string{"scanme.sh"}}
	argv := req.Argv()

	for _, want := range []string{
		"-severity", "critical,high,medium",
		"-rl", "20",
		"-c", "10",
		"scanme.sh",
	} {
		if !slices.Contains(argv, want) {
			t.Errorf("argv %q does not contain %q", argv, want)
		}
	}
	if slices.Contains(argv, "-headless") {
		t.Errorf("argv %q enables headless by default", argv)
	}
	if argv[len(argv)-1] != "scanme.sh" {
		t.Errorf("target is not last: %q", argv)
	}
}

func TestNucleiArgvOverrides(t *testing.T) {
	t.Parallel()

	req := NucleiRequest{
		Targets:     []string{"https://a.example.com", "b.example.com"},
		Severity:    []string{"critical", "high"},
		Tags:        []string{"cve", "exposure"},
		Templates:   []string{"/opt/templates"},
		ExcludeTags: []string{"dos"},
		RateLimit:   150,
		Concurrency: 25,
		Headless:    true,
	}
	argv := req.Argv()

	for _, want := range []string{
		"-severity", "critical,high",
		"-tags", "cve", "-tags", "exposure",
		"-t", "/opt/templates",
		"-etags", "dos",
		"-rl", "100",
		"-c", "25",
		"-headless",
		"https://a.example.com",
		"b.example.com",
	} {
		if !slices.Contains(argv, want) {
			t.Errorf("argv %q does not contain %q", argv, want)
		}
	}
}

func TestNucleiValidate(t *testing.T) {
	t.Parallel()

	overCap := make([]string, NucleiMaxTargets+1)
	for i := range overCap {
		overCap[i] = "host.example.com"
	}
	templatesDir := t.TempDir()
	cases := []struct {
		name string
		req  NucleiRequest
		want string
	}{
		{
			name: "empty targets",
			req:  NucleiRequest{},
			want: "targets is required",
		},
		{
			name: "over cap",
			req:  NucleiRequest{Targets: overCap},
			want: "201 targets exceeds the 200-target cap",
		},
		{
			name: "relative template path",
			req: NucleiRequest{
				Targets:   []string{"scanme.sh"},
				Templates: []string{"templates/cves"},
			},
			want: "must be an absolute template directory or file",
		},
		{
			name: "nonexistent template path",
			req: NucleiRequest{
				Targets:   []string{"scanme.sh"},
				Templates: []string{filepath.Join(templatesDir, "missing")},
			},
			want: "does not exist or is not readable",
		},
		{
			name: "negative rate limit",
			req: NucleiRequest{
				Targets:   []string{"scanme.sh"},
				RateLimit: -1,
			},
			want: "rate_limit must be >= 0",
		},
		{
			name: "existing absolute template accepted",
			req: NucleiRequest{
				Targets:   []string{"scanme.sh"},
				Templates: []string{templatesDir},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.req.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestCompactNuclei(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"template-id":"exposed-config","info":{"name":"Exposed Config","severity":"high","tags":["config","exposure"],"description":"A configuration file is publicly readable."},"type":"http","host":"scanme.sh","ip":"1.2.3.4","matched-at":"https://scanme.sh/.env","matcher-name":"word","extracted-results":["api_key=AKIAABCDEFGHIJKLMNOP","Authorization: Bearer abc123def456","plain-value"]}` + "\n")
	entries, truncated, dropped := decodeEntries(raw, 200)
	if truncated != 0 || dropped != 0 {
		t.Fatalf("truncated=%d dropped=%d, want 0/0", truncated, dropped)
	}
	result := CompactNuclei(Result{Entries: entries, Count: len(entries)})
	if result.Count != 1 {
		t.Fatalf("Count = %d, want 1", result.Count)
	}

	f := result.Entries[0]
	for _, field := range []struct {
		name string
		got  string
		want string
	}{
		{"template", f.Template, "exposed-config"},
		{"name", f.Name, "Exposed Config"},
		{"severity", f.Severity, "high"},
		{"url", f.URL, "https://scanme.sh/.env"},
		{"matcher", f.Matcher, "word"},
		{"type", f.Type, "http"},
		{"host", f.Host, "scanme.sh"},
		{"ip", f.IP, "1.2.3.4"},
	} {
		if field.got != field.want {
			t.Errorf("%s = %q, want %q", field.name, field.got, field.want)
		}
	}
	if !slices.Equal(f.Tags, []string{"config", "exposure"}) {
		t.Errorf("Tags = %q", f.Tags)
	}

	wantExtracted := []string{
		"api_key=" + NucleiRedacted,
		"Authorization: Bearer " + NucleiRedacted,
		"plain-value",
	}
	if !slices.Equal(f.Extracted, wantExtracted) {
		t.Errorf("Extracted = %q, want %q", f.Extracted, wantExtracted)
	}
}

func TestCompactNucleiClipsDescription(t *testing.T) {
	t.Parallel()

	entry := map[string]any{
		"template-id": "long",
		"info": map[string]any{
			"description": strings.Repeat("a", 400),
		},
	}
	f := nucleiFinding(entry)
	if len(f.Description) != nucleiFindingClip+len("…") {
		t.Fatalf("description length = %d, want %d", len(f.Description), nucleiFindingClip+len("…"))
	}
	if !strings.HasSuffix(f.Description, "…") {
		t.Errorf("description %q is not marked as clipped", f.Description)
	}
}

func TestTemplatesMissing(t *testing.T) {
	t.Parallel()

	if TemplatesMissing(nil) {
		t.Error("nil error reported as missing templates")
	}
	if !TemplatesMissing(errTest("nuclei: no templates found for scan")) {
		t.Error("a no-templates error was not recognized")
	}
	if TemplatesMissing(errTest("nuclei: connection refused")) {
		t.Error("an unrelated error was reported as missing templates")
	}
}

func TestHostsOfTargets(t *testing.T) {
	t.Parallel()

	got := HostsOfTargets([]string{
		"https://a.example.com/path",
		"a.example.com:8443",
		"A.example.com",
		"b.example.com",
		"",
	})
	if want := []string{"a.example.com", "b.example.com"}; !slices.Equal(got, want) {
		t.Errorf("HostsOfTargets = %q, want %q", got, want)
	}
}

// errTest is a minimal error for marker tests.
type errTest string

func (e errTest) Error() string { return string(e) }

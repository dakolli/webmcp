package pageops

import (
	"slices"
	"strings"
	"testing"

	"webmcp/pkg/browsers"
)

// cspBypassIDs pins the probe order and count so the report stays deterministic.
var cspBypassIDs = []string{
	"csp_bypass_unsafe_inline",
	"csp_bypass_unsafe_eval",
	"csp_bypass_script_wildcard",
	"csp_bypass_object_src",
	"csp_bypass_base_uri",
	"csp_bypass_frame_ancestors",
	"csp_bypass_report_only",
	"csp_bypass_trusted_host",
}

// TestSecurityAspectBounds pins the two behaviors that keep the audit honest:
// HSTS max-age parsing and the explicit "+N more" marker when evidence is
// capped, so a cut is never silent.
func TestSecurityAspectBounds(t *testing.T) {
	t.Run("hsts max-age parsing", func(t *testing.T) {
		tests := []struct {
			name  string
			value string
			want  bool
		}{
			{"zero disables", "max-age=0", true},
			{"quoted zero disables", `max-age="0"`, true},
			{"zero after includeSubDomains", "includeSubDomains; max-age=0", true},
			{"positive age keeps hsts", "max-age=31536000; includeSubDomains", false},
			{"no max-age keeps hsts", "includeSubDomains", false},
			{"empty keeps hsts", "", false},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := hstsDisabled(tt.value); got != tt.want {
					t.Errorf("hstsDisabled(%q) = %t, want %t", tt.value, got, tt.want)
				}
			})
		}
	})

	t.Run("evidence cap reports omitted", func(t *testing.T) {
		offenders := make([]string, securityEvidenceLimit+3)
		for i := range offenders {
			offenders[i] = "http://example.test/" + strings.Repeat("x", i+1)
		}
		check := securityFinding("mixed_content", "ok", "%d offenders", offenders)
		if check.Status != "warn" {
			t.Fatalf("status = %q, want warn", check.Status)
		}
		if !strings.HasSuffix(check.Evidence, "(+3 more)") {
			t.Errorf("evidence = %q, want it to end with the omitted count", check.Evidence)
		}
		if lines := strings.Count(check.Evidence, "\n"); lines != securityEvidenceLimit {
			t.Errorf("evidence has %d lines, want %d values plus the omitted marker", lines, securityEvidenceLimit)
		}
	})
}

// cspChecksByID indexes the probe results so a case can assert one finding
// without restating the whole report.
func cspChecksByID(policy, reportOnly string) map[string]securityCheck {
	checks := securityCSPBypassChecks(policy, reportOnly)
	byID := make(map[string]securityCheck, len(checks))
	for _, c := range checks {
		byID[c.ID] = c
	}
	return byID
}

// TestSecurityCSPBypassOrder pins the fixed order, count and repeatability of
// the probes so two runs over the same header produce identical output.
func TestSecurityCSPBypassOrder(t *testing.T) {
	first := securityCSPBypassChecks("script-src 'self' 'unsafe-inline'", "")
	if len(first) != len(cspBypassIDs) {
		t.Fatalf("got %d checks, want %d", len(first), len(cspBypassIDs))
	}
	for i, id := range cspBypassIDs {
		if first[i].ID != id {
			t.Errorf("check %d = %q, want %q", i, first[i].ID, id)
		}
	}
	second := securityCSPBypassChecks("script-src 'self' 'unsafe-inline'", "")
	if !slices.Equal(first, second) {
		t.Error("two runs over the same policy produced different results")
	}
}

// TestSecurityCSPBypassStrictPolicy proves a well-formed policy passes every
// probe and leaves only the report-only check as info.
func TestSecurityCSPBypassStrictPolicy(t *testing.T) {
	policy := "default-src 'self'; script-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"
	want := map[string]string{
		"csp_bypass_unsafe_inline":   "ok",
		"csp_bypass_unsafe_eval":     "ok",
		"csp_bypass_script_wildcard": "ok",
		"csp_bypass_object_src":      "ok",
		"csp_bypass_base_uri":        "ok",
		"csp_bypass_frame_ancestors": "ok",
		"csp_bypass_report_only":     "info",
		"csp_bypass_trusted_host":    "ok",
	}
	got := cspChecksByID(policy, "")
	for id, status := range want {
		check, ok := got[id]
		if !ok {
			t.Fatalf("missing check %q", id)
		}
		if check.Status != status {
			t.Errorf("%s status = %q, want %q (detail %q)", id, check.Status, status, check.Detail)
		}
	}
}

// TestSecurityCSPBypassNoPolicy proves the aspect degrades to info with empty
// evidence when the page declares no policy.
func TestSecurityCSPBypassNoPolicy(t *testing.T) {
	for _, c := range securityCSPBypassChecks("", "") {
		if c.Status != "info" {
			t.Errorf("%s status = %q, want info", c.ID, c.Status)
		}
		if c.Evidence != "" {
			t.Errorf("%s evidence = %q, want empty", c.ID, c.Evidence)
		}
	}
}

// TestSecurityCSPBypassDetection is the bypass matrix: each case selects one
// finding from a real header string and pins its status and evidence fragment.
func TestSecurityCSPBypassDetection(t *testing.T) {
	tests := []struct {
		name       string
		policy     string
		reportOnly string
		wantID     string
		wantStatus string
		wantQuote  string
	}{
		{"unsafe-inline", "script-src 'self' 'unsafe-inline'; object-src 'none'", "", "csp_bypass_unsafe_inline", "fail", "'unsafe-inline'"},
		{"unsafe-inline neutralized by nonce", "script-src 'self' 'unsafe-inline' 'nonce-abc'; object-src 'none'", "", "csp_bypass_unsafe_inline", "ok", "'nonce-abc'"},
		{"unsafe-inline neutralized by hash", "script-src 'self' 'unsafe-inline' 'sha256-abc'; object-src 'none'", "", "csp_bypass_unsafe_inline", "ok", "'sha256-abc'"},
		{"unsafe-eval", "script-src 'self' 'unsafe-eval'; object-src 'none'", "", "csp_bypass_unsafe_eval", "fail", "'unsafe-eval'"},
		{"wildcard star", "script-src *; object-src 'none'", "", "csp_bypass_script_wildcard", "fail", "script-src *"},
		{"scheme source", "script-src https:; object-src 'none'", "", "csp_bypass_script_wildcard", "fail", "https:"},
		{"host wildcard", "script-src *.example.com; object-src 'none'", "", "csp_bypass_script_wildcard", "warn", "*.example.com"},
		{"missing object-src", "script-src 'self'; base-uri 'none'", "", "csp_bypass_object_src", "warn", ""},
		{"eventual object-src fallback", "default-src 'self'", "", "csp_bypass_object_src", "ok", "default-src 'self'"},
		{"missing base-uri", "script-src 'self'; object-src 'none'", "", "csp_bypass_base_uri", "warn", ""},
		{"frame-ancestors wildcard", "script-src 'self'; object-src 'none'; frame-ancestors *", "", "csp_bypass_frame_ancestors", "warn", "frame-ancestors *"},
		{"frame-ancestors absent", "script-src 'self'; object-src 'none'", "", "csp_bypass_frame_ancestors", "info", ""},
		{"trusted jsonp host", "script-src https://ajax.googleapis.com; object-src 'none'", "", "csp_bypass_trusted_host", "warn", "https://ajax.googleapis.com"},
		{"untrusted host", "script-src https://cdn.example.com; object-src 'none'", "", "csp_bypass_trusted_host", "ok", "cdn.example.com"},
		{"report-only policy", "script-src 'self'; object-src 'none'", "script-src 'self'; object-src 'none'", "csp_bypass_report_only", "info", "script-src 'self'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check, ok := cspChecksByID(tt.policy, tt.reportOnly)[tt.wantID]
			if !ok {
				t.Fatalf("missing check %q", tt.wantID)
			}
			if check.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q (detail %q)", check.Status, tt.wantStatus, check.Detail)
			}
			if tt.wantQuote != "" && !strings.Contains(check.Evidence, tt.wantQuote) {
				t.Errorf("evidence = %q, want it to quote %q", check.Evidence, tt.wantQuote)
			}
		})
	}
}

// TestSecurityCSPReportOnlyExplains proves a report-only policy that would block
// inline scripts says so instead of reading as enforced.
func TestSecurityCSPReportOnlyExplains(t *testing.T) {
	check := cspChecksByID("", "script-src 'self'")["csp_bypass_report_only"]
	if !strings.Contains(check.Detail, "would otherwise block") {
		t.Errorf("detail = %q, want it to name the unenforced block", check.Detail)
	}
}

// TestSecurityCSPBypassEvidenceQuotesPolicy proves no evidence value is ever
// invented: every evidence line is a fragment of the real policy string.
func TestSecurityCSPBypassEvidenceQuotesPolicy(t *testing.T) {
	tests := []struct {
		name       string
		policy     string
		reportOnly string
	}{
		{
			name:   "mixed policy",
			policy: "script-src 'self' 'unsafe-inline' 'unsafe-eval' https://ajax.googleapis.com *.example.com; object-src 'none'; frame-ancestors *",
		},
		{
			name:       "report-only only",
			reportOnly: "script-src 'self' 'unsafe-eval'",
		},
		{
			name:   "default-src only",
			policy: "default-src 'self'",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, c := range securityCSPBypassChecks(tt.policy, tt.reportOnly) {
				for _, line := range strings.Split(c.Evidence, "\n") {
					if line == "" {
						continue
					}
					if !strings.Contains(tt.policy, line) && !strings.Contains(tt.reportOnly, line) {
						t.Errorf("%s evidence %q is not a fragment of the policy", c.ID, line)
					}
				}
			}
		})
	}
}

// TestCSPSourceHost pins source-token normalization and the bypass-host match.
func TestCSPSourceHost(t *testing.T) {
	hosts := []struct {
		token string
		want  string
	}{
		{"https://ajax.googleapis.com", "ajax.googleapis.com"},
		{"ajax.googleapis.com", "ajax.googleapis.com"},
		{"*.google.com", "google.com"},
		{"https://cdn.example.com:8443/app.js", "cdn.example.com"},
		{"'self'", ""},
		{"'nonce-abc'", ""},
		{"https:", ""},
		{"data:", ""},
		{"*", ""},
	}
	for _, tt := range hosts {
		if got := cspSourceHost(tt.token); got != tt.want {
			t.Errorf("cspSourceHost(%q) = %q, want %q", tt.token, got, tt.want)
		}
	}

	matches := []struct {
		host string
		want bool
	}{
		{"ajax.googleapis.com", true},
		{"sub.google.com", true},
		{"google.com", true},
		{"notgoogle.com", false},
		{"cdn.example.com", false},
	}
	for _, tt := range matches {
		if _, got := cspBypassHostFor(tt.host); got != tt.want {
			t.Errorf("cspBypassHostFor(%q) matched = %t, want %t", tt.host, got, tt.want)
		}
	}
}

// securityCORSPair is the wildcard-origin-plus-credentials combination the CORS
// check flags.
var securityCORSPair = map[string]string{
	"Access-Control-Allow-Origin":      "*",
	"Access-Control-Allow-Credentials": "true",
}

// TestSecurityAuditScopesNetworkToCurrentDocument proves the audit cannot cite
// an earlier navigation's requests: only the document request that produced the
// audited page and the requests after it supply evidence.
func TestSecurityAuditScopesNetworkToCurrentDocument(t *testing.T) {
	tests := []struct {
		name       string
		pageURL    string
		ring       []browsers.NetworkEntry
		wantCORS   string
		wantMixed  string
		wantAbsent []string
	}{
		{
			name:    "second host after a POST",
			pageURL: "https://new.test/",
			ring: []browsers.NetworkEntry{
				{Seq: 1, Method: "POST", Type: "Document", URL: "https://old.test/post"},
				{Seq: 2, Type: "XHR", URL: "https://old.test/api", RespHeaders: securityCORSPair},
				{Seq: 3, Type: "Image", URL: "http://old.test/favicon.ico"},
				{Seq: 4, Type: "Document", URL: "https://new.test/"},
				{Seq: 5, Type: "XHR", URL: "https://new.test/api", RespHeaders: securityCORSPair},
			},
			wantCORS:   "https://new.test/api",
			wantMixed:  "",
			wantAbsent: []string{"old.test"},
		},
		{
			name:    "same origin second navigation",
			pageURL: "https://same.test/b",
			ring: []browsers.NetworkEntry{
				{Seq: 1, Type: "Document", URL: "https://same.test/a"},
				{Seq: 2, Type: "XHR", URL: "https://same.test/a/api", RespHeaders: securityCORSPair},
				{Seq: 3, Type: "Script", URL: "http://same.test/a/app.js"},
				{Seq: 4, Type: "Document", URL: "https://same.test/b"},
				{Seq: 5, Type: "XHR", URL: "https://same.test/b/api", RespHeaders: securityCORSPair},
			},
			wantCORS:   "https://same.test/b/api",
			wantMixed:  "",
			wantAbsent: []string{"https://same.test/a"},
		},
		{
			name:    "fragment in the audited URL",
			pageURL: "https://frag.test/page#section",
			ring: []browsers.NetworkEntry{
				{Seq: 1, Type: "Document", URL: "https://old.test/"},
				{Seq: 2, Type: "Document", URL: "https://frag.test/page"},
				{Seq: 3, Type: "XHR", URL: "https://frag.test/api", RespHeaders: securityCORSPair},
			},
			wantCORS:   "https://frag.test/api",
			wantMixed:  "",
			wantAbsent: []string{"old.test"},
		},
		{
			name:    "client-side route change keeps the loaded document",
			pageURL: "https://spa.test/dashboard",
			ring: []browsers.NetworkEntry{
				{Seq: 1, Type: "Document", URL: "https://spa.test/"},
				{Seq: 2, Type: "XHR", URL: "https://spa.test/api/me", RespHeaders: securityCORSPair},
			},
			wantCORS:  "https://spa.test/api/me",
			wantMixed: "",
		},
		{
			name:    "same-origin iframe does not move the boundary",
			pageURL: "https://app.test/",
			ring: []browsers.NetworkEntry{
				{Seq: 1, Type: "Document", URL: "https://app.test/"},
				{Seq: 2, Type: "XHR", URL: "https://app.test/api", RespHeaders: securityCORSPair},
				{Seq: 3, Type: "Document", URL: "https://app.test/widget"},
			},
			wantCORS:  "https://app.test/api",
			wantMixed: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, scoped, found := securityDocumentRing(tt.ring, tt.pageURL)
			if !found {
				t.Fatalf("securityDocumentRing(%q) did not find the audited document", tt.pageURL)
			}
			cors := securityCORSCheck(scoped)
			mixed := securityMixedContentCheck(securityProbe{Scheme: "https"}, scoped)
			if cors.ID != "cors" || mixed.ID != "mixed_content" {
				t.Fatalf("check ids = %q, %q; want cors, mixed_content", cors.ID, mixed.ID)
			}
			if cors.Evidence != tt.wantCORS {
				t.Errorf("cors evidence = %q, want %q", cors.Evidence, tt.wantCORS)
			}
			if mixed.Evidence != tt.wantMixed {
				t.Errorf("mixed_content evidence = %q, want %q", mixed.Evidence, tt.wantMixed)
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(cors.Evidence, absent) {
					t.Errorf("cors evidence %q cites the previous document (%q)", cors.Evidence, absent)
				}
				if strings.Contains(mixed.Evidence, absent) {
					t.Errorf("mixed_content evidence %q cites the previous document (%q)", mixed.Evidence, absent)
				}
			}
			if cors2 := securityCORSCheck(scoped); !slices.Equal([]securityCheck{cors}, []securityCheck{cors2}) {
				t.Error("two audits of the same ring produced different cors findings")
			}
		})
	}
}

// TestSecurityAuditEmptyRingHasEmptyEvidence proves a ring with nothing for the
// audited document yields empty evidence instead of an older page's request.
func TestSecurityAuditEmptyRingHasEmptyEvidence(t *testing.T) {
	tests := []struct {
		name    string
		pageURL string
		ring    []browsers.NetworkEntry
	}{
		{name: "empty ring", pageURL: "https://new.test/"},
		{
			name:    "only an older document's requests",
			pageURL: "https://new.test/",
			ring: []browsers.NetworkEntry{
				{Seq: 1, Method: "POST", Type: "Document", URL: "https://old.test/post"},
				{Seq: 2, Type: "XHR", URL: "https://old.test/api", RespHeaders: securityCORSPair},
				{Seq: 3, Type: "Image", URL: "http://old.test/favicon.ico"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, scoped, found := securityDocumentRing(tt.ring, tt.pageURL)
			if found {
				t.Fatalf("securityDocumentRing found a document for %q", tt.pageURL)
			}
			if doc.URL != "" || len(scoped) != 0 {
				t.Fatalf("securityDocumentRing returned doc %q and %d scoped entries", doc.URL, len(scoped))
			}
			cors := securityCORSCheck(scoped)
			mixed := securityMixedContentCheck(securityProbe{Scheme: "https"}, scoped)
			if cors.Status != "info" {
				t.Errorf("cors status = %q, want info", cors.Status)
			}
			if mixed.Status != "ok" {
				t.Errorf("mixed_content status = %q, want ok", mixed.Status)
			}
			if cors.Evidence != "" || mixed.Evidence != "" {
				t.Errorf("evidence = %q, %q; want empty", cors.Evidence, mixed.Evidence)
			}
		})
	}
}

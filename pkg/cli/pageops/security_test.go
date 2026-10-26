package pageops

import (
	"strings"
	"testing"
)

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

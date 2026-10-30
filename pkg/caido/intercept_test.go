package caido

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// TestInterceptRuleTargetsSkipsForeignPrefix pins the kill switch to a prefix
// match, so a foreign rule whose name merely contains the prefix survives.
func TestInterceptRuleTargetsSkipsForeignPrefix(t *testing.T) {
	rules := []InterceptRule{
		{ID: "1", Name: interceptRulePrefix + " status 599"},
		{ID: "2", Name: "human-webmcp-intercept-x"},
		{ID: "3", Name: "my-webmcp-intercept-backup"},
	}
	got := interceptRuleTargets(rules)
	if !slices.Equal(got, []string{"1"}) {
		t.Fatalf("targets = %v, want [1]", got)
	}
}

// TestInterceptFilterConflictCarriesHint pins that a live filter conflict keeps
// its invalid_args prose and supplies its own remediation.
func TestInterceptFilterConflictCarriesHint(t *testing.T) {
	state := Intercept{
		Response: InterceptDirection{
			Enabled: true,
			Filter:  `req.host.eq:"live.example"`,
		},
	}
	err := interceptFilterConflict(state, `req.host.eq:"example.com"`)
	if err == nil {
		t.Fatal("interceptFilterConflict() = nil, want a conflict")
	}
	if !strings.Contains(err.Error(), "intercept is already enabled") {
		t.Fatalf("message = %q, want the invalid_args marker", err.Error())
	}
	var hinted interface{ Hint() string }
	if !errors.As(err, &hinted) {
		t.Fatalf("error %T does not carry Hint()", err)
	}
	if got := hinted.Hint(); got != "clear the live rules before you arm a different filter" {
		t.Fatalf("hint = %q", got)
	}
}

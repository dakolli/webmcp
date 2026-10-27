package pageops

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestInputConfirmationNeverEchoesValue walks every input/type result path and
// proves the typed value never reaches the confirmation string: a password or
// hidden target adds value_redacted=true, and no path echoes the value itself.
func TestInputConfirmationNeverEchoesValue(t *testing.T) {
	const secret = "hunter2-SECRET"
	wantLen := utf8.RuneCountInString(secret)

	tests := []struct {
		name     string
		verb     string
		handle   string
		selector string
		secret   bool
		redacted bool
	}{
		{name: "input selector text", verb: "set input", selector: "input#email"},
		{name: "input selector password", verb: "set input", selector: "input#password", secret: true, redacted: true},
		{name: "input handle password", verb: "set input", handle: "g1-b2", secret: true, redacted: true},
		{name: "type selector password", verb: "typed", selector: "input#password", secret: true, redacted: true},
		{name: "type handle text", verb: "typed", handle: "g1-b2"},
		{name: "type handle hidden", verb: "typed", handle: "g1-b2", secret: true, redacted: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inputConfirmation(tt.verb, tt.handle, tt.selector, secret, tt.secret)
			if strings.Contains(got, secret) {
				t.Fatalf("confirmation leaked the typed value: %q", got)
			}
			if strings.Contains(got, "value=") {
				t.Fatalf("confirmation carries a value field: %q", got)
			}
			if want := fmt.Sprintf("length=%d", wantLen); !strings.Contains(got, want) {
				t.Fatalf("confirmation = %q, want %s", got, want)
			}
			if gotRedacted := strings.Contains(got, "value_redacted=true"); gotRedacted != tt.redacted {
				t.Fatalf("value_redacted = %v, want %v (%q)", gotRedacted, tt.redacted, got)
			}
		})
	}
}

// TestInputConfirmationRuneLength pins that the reported length counts runes,
// not bytes, so a multi-byte value is described by its character count.
func TestInputConfirmationRuneLength(t *testing.T) {
	got := inputConfirmation("set input", "", "input#name", "héllo", false)
	if want := "length=5"; !strings.Contains(got, want) {
		t.Fatalf("confirmation = %q, want %s", got, want)
	}
}

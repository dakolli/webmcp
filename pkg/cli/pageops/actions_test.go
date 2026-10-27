package pageops

import (
	"slices"
	"testing"
)

// TestActions pins the page-act table: the expected row count, unique names,
// dashed lookup and one dispatch case per row.
func TestActions(t *testing.T) {
	const wantRows = 36
	table := Actions()
	if len(table) != wantRows {
		t.Fatalf("action table has %d rows, want %d", len(table), wantRows)
	}

	seen := make(map[string]bool, len(table))
	for _, a := range table {
		if a.Name == "" {
			t.Error("action row has an empty name")
			continue
		}
		if seen[a.Name] {
			t.Errorf("duplicate action name %q", a.Name)
		}
		seen[a.Name] = true
		if a.Summary == "" {
			t.Errorf("action %q has no summary", a.Name)
		}
	}

	names := ActionNames()
	if len(names) != len(table) {
		t.Fatalf("ActionNames has %d entries, want %d", len(names), len(table))
	}
	for i, a := range table {
		if names[i] != a.Name {
			t.Errorf("ActionNames[%d] = %q, want %q", i, names[i], a.Name)
		}
	}

	for _, spelling := range []string{"poc_bundle", "poc-bundle"} {
		a, ok := LookupAction(spelling)
		if !ok {
			t.Errorf("LookupAction(%q) not found", spelling)
			continue
		}
		if a.Name != "poc_bundle" {
			t.Errorf("LookupAction(%q).Name = %q, want poc_bundle", spelling, a.Name)
		}
	}
	if _, ok := LookupAction("not_an_action"); ok {
		t.Error("LookupAction(not_an_action) found an unknown action")
	}

	for _, name := range names {
		if !slices.Contains(dispatchActions, name) {
			t.Errorf("action %q has no dispatch case", name)
		}
	}
	for _, name := range dispatchActions {
		if _, ok := findAction(name); !ok {
			t.Errorf("dispatch case %q is missing from the action table", name)
		}
	}
}

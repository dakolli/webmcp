package tools

import "testing"

// TestAspectRegistry pins the registry invariants that keep a new aspect to one
// registerAspect line: unique names in registration order, a reader for every
// name, and a schema enum that mirrors the registration order.
func TestAspectRegistry(t *testing.T) {
	const wantList = "html, dom, text, css, js, links, screenshot, meta, interactives, a11y, network, body, console, article, marks, security"
	if got := validAspects(); got != wantList {
		t.Errorf("validAspects() = %q, want %q", got, wantList)
	}
	if len(aspectNames) != len(aspects) {
		t.Fatalf("aspectNames has %d entries, aspects has %d", len(aspectNames), len(aspects))
	}
	for _, name := range aspectNames {
		if a, ok := aspects[name]; !ok || a.read == nil {
			t.Errorf("aspect %q is registered without a reader", name)
		}
	}

	what, ok := (&Page{}).InputSchema().Properties["what"].(map[string]any)
	if !ok {
		t.Fatal("what schema property is missing")
	}
	enum, ok := what["enum"].([]string)
	if !ok {
		t.Fatal("what schema property has no enum")
	}
	if len(enum) != len(aspectNames) {
		t.Fatalf("schema enum has %d values, registry has %d", len(enum), len(aspectNames))
	}
	for i, name := range aspectNames {
		if enum[i] != name {
			t.Errorf("schema enum[%d] = %q, want %q", i, enum[i], name)
		}
	}
}

package pageops

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// TestFormsAspectRegistered pins the registry entry: forms validates, owns its
// own caps, and is named by the shared unknown-what error.
func TestFormsAspectRegistered(t *testing.T) {
	if err := ValidateAspect("forms"); err != nil {
		t.Fatalf("ValidateAspect(forms) = %v, want nil", err)
	}

	selfCapped := false
	for _, a := range aspectEntries {
		if a.name == "forms" {
			selfCapped = a.selfCapped
		}
	}
	if !selfCapped {
		t.Fatal("forms entry is not self-capped")
	}

	err := ValidateAspect("not-a-form")
	if err == nil || !strings.Contains(err.Error(), "forms") {
		t.Fatalf("unknown-what error = %v, want it to list forms", err)
	}
}

// TestShapeFormsNoValueLeak proves a control value on the probe wire is ignored:
// the raw decoder has no value field and the output reports has_value only.
func TestShapeFormsNoValueLeak(t *testing.T) {
	const probe = `{"form_total":1,"forms":[{
		"action":"https://example.com/login",
		"method":"post",
		"enctype":"application/x-www-form-urlencoded",
		"field_total":2,
		"fields":[
			{"name":"user","type":"text","label":"User","has_value":false,"options":[]},
			{"name":"password","type":"password","label":"Password","has_value":true,"value":"hunter2","options":[]}
		],
		"submits":[]
	}]}`

	var raw rawForms
	if err := json.Unmarshal([]byte(probe), &raw); err != nil {
		t.Fatalf("decode probe: %v", err)
	}

	out, err := json.Marshal(shapeForms(raw))
	if err != nil {
		t.Fatalf("marshal shaped forms: %v", err)
	}
	if strings.Contains(string(out), "hunter2") {
		t.Fatalf("form output leaked a field value: %s", out)
	}
	if strings.Contains(string(out), `"value":`) {
		t.Fatalf("form output carries a value key: %s", out)
	}

	var got FormsResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode shaped forms: %v", err)
	}
	fields := got.Forms[0].Fields
	if len(fields) != 2 {
		t.Fatalf("fields = %d, want 2", len(fields))
	}
	if fields[1].Name != "password" || !fields[1].HasValue {
		t.Fatalf("password field = %+v, want name=password has_value=true", fields[1])
	}
}

// TestShapeFormsDroppedCounts pins the form and field cap counts and that both
// counters are always present in the JSON envelope.
func TestShapeFormsDroppedCounts(t *testing.T) {
	t.Run("form cap", func(t *testing.T) {
		raw := rawForms{FormTotal: 25, Forms: make([]rawForm, 25)}
		got := shapeForms(raw)
		if len(got.Forms) != formMaxForms {
			t.Fatalf("forms = %d, want %d", len(got.Forms), formMaxForms)
		}
		if got.FormsDropped != 5 {
			t.Fatalf("forms_dropped = %d, want 5", got.FormsDropped)
		}
		if got.FieldsDropped != 0 {
			t.Fatalf("fields_dropped = %d, want 0", got.FieldsDropped)
		}
	})

	t.Run("field cap", func(t *testing.T) {
		fields := make([]rawField, 600)
		raw := rawForms{
			FormTotal: 1,
			Forms:     []rawForm{{FieldTotal: 600, Fields: fields}},
		}
		got := shapeForms(raw)
		if len(got.Forms[0].Fields) != formMaxFields {
			t.Fatalf("fields = %d, want %d", len(got.Forms[0].Fields), formMaxFields)
		}
		if got.FieldsDropped != 100 {
			t.Fatalf("fields_dropped = %d, want 100", got.FieldsDropped)
		}
		if got.FormsDropped != 0 {
			t.Fatalf("forms_dropped = %d, want 0", got.FormsDropped)
		}
	})

	t.Run("wire always carries counters", func(t *testing.T) {
		raw := rawForms{FormTotal: 22, Forms: []rawForm{{}}}
		out, err := json.Marshal(shapeForms(raw))
		if err != nil {
			t.Fatalf("marshal shaped forms: %v", err)
		}
		for _, want := range []string{`"forms_dropped":21`, `"fields_dropped":0`, `"truncated":true`} {
			if !strings.Contains(string(out), want) {
				t.Errorf("envelope %s is missing %s", out, want)
			}
		}
	})
}

// TestCoerceField pins the value coercion per control type: the write kind and
// the events the page dispatches afterwards.
func TestCoerceField(t *testing.T) {
	cases := []struct {
		name   string
		tag    string
		typ    string
		value  string
		kind   fieldWriteKind
		events []string
	}{
		{"text", "input", "text", "hi", fieldWriteValue, []string{"input", "change"}},
		{"email", "input", "email", "a@b.c", fieldWriteValue, []string{"input", "change"}},
		{"checkbox", "input", "checkbox", "1", fieldWriteChecked, []string{"change"}},
		{"radio", "input", "radio", "1", fieldWriteChecked, []string{"change"}},
		{"select", "select", "", "opt", fieldWriteSelect, []string{"change"}},
		{"textarea", "textarea", "", "line", fieldWriteTextarea, []string{"input"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := coerceField("#f", tc.tag, tc.typ, tc.value)
			if got.Kind != string(tc.kind) {
				t.Errorf("kind = %q, want %q", got.Kind, tc.kind)
			}
			if !slices.Equal(got.Events, tc.events) {
				t.Errorf("events = %v, want %v", got.Events, tc.events)
			}
			if tc.kind == fieldWriteChecked && got.Value != "" {
				t.Errorf("checked value = %q, want empty", got.Value)
			}
			if tc.kind != fieldWriteChecked && got.Value != tc.value {
				t.Errorf("value = %q, want %q", got.Value, tc.value)
			}
		})
	}
}
func TestShapeFormsCSRF(t *testing.T) {
	raw := rawForms{
		FormTotal: 1,
		Forms: []rawForm{{
			Fields: []rawField{
				{Name: "csrf_token", Type: "hidden", Hidden: true},
				{Name: "whatever", Type: "hidden", Hidden: true, Token: true},
				{Name: "foo", Type: "hidden", Hidden: true},
				{Name: "token", Type: "text"},
			},
		}},
	}

	got := shapeForms(raw)
	cands := got.Forms[0].CSRFCandidates
	if len(cands) != 2 {
		t.Fatalf("csrf candidates = %+v, want 2 entries", cands)
	}
	if cands[0].Name != "csrf_token" || cands[0].Match != "name" {
		t.Errorf("candidate[0] = %+v, want csrf_token matched by name", cands[0])
	}
	if cands[1].Name != "whatever" || cands[1].Match != "value" {
		t.Errorf("candidate[1] = %+v, want whatever matched by value", cands[1])
	}
}

// TestFillFormResultHasNoDeltaSuffix pins that fill_form returns its JSON
// envelope untouched: the result already carries url_after, so no state delta
// may be appended. On the old code the suffix made the output invalid JSON and
// the MCP envelope downgraded it to a string.
func TestFillFormResultHasNoDeltaSuffix(t *testing.T) {
	before := PageState{URL: "https://example.com", Title: "Home"}
	after := PageState{URL: "https://example.com/?q=wm-probe", Title: "Home"}

	for _, action := range []string{"navigate", "eval", "poc_bundle", "fill_form"} {
		t.Run(action, func(t *testing.T) {
			if got := ActStateDeltaFor(action, before, after); got != "" {
				t.Fatalf("ActStateDeltaFor(%q) = %q, want no suffix", action, got)
			}
		})
	}

	const result = `{"form":"form#wmf","filled":[],"skipped":[],"submit":"requestSubmit","url_after":"https://example.com/?q=wm-probe"}`
	out := result + ActStateDeltaFor("fill_form", before, after)
	if out != result {
		t.Fatalf("fill_form output = %q, want %q", out, result)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("fill_form output is not a JSON object: %v", err)
	}
	if obj["url_after"] != "https://example.com/?q=wm-probe" {
		t.Fatalf("url_after = %v, want the landed url", obj["url_after"])
	}
	if got := ActStateDeltaFor("click", before, after); got == "" {
		t.Fatal("click lost its state delta")
	}
}

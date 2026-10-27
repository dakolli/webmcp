package pageops

import (
	"encoding/json"
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

// TestShapeFormsCSRF pins the candidate rule: only hidden inputs whose name or
// value looks like a token qualify.
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

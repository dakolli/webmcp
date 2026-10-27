package pageops

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"webmcp/pkg/browsers"
)

const (
	// formMaxForms bounds how many forms one response reports; the dropped
	// remainder is reported through forms_dropped.
	formMaxForms = 20
	// formMaxFields bounds the fields reported per form; the dropped remainder
	// is reported through fields_dropped.
	formMaxFields = 500
	// formMaxOptions bounds the option list reported per select field.
	formMaxOptions = 100
	// formMaxSubmits bounds the submit controls reported per form.
	formMaxSubmits = 50
	// formMaxCSRF bounds the CSRF candidates reported per form.
	formMaxCSRF = 50

	// formSafetyForms bounds how many forms the in-page probe collects.
	formSafetyForms = 100
	// formSafetyFields bounds how many fields the in-page probe collects per
	// form.
	formSafetyFields = 2000
	// formSafetyOptions bounds how many options the in-page probe collects per
	// select.
	formSafetyOptions = 200
	// formSafetySubmits bounds how many submit controls the in-page probe
	// collects per form.
	formSafetySubmits = 200
)

// tokenNamePattern matches hidden input names that conventionally carry a CSRF
// or anti-automation token.
var tokenNamePattern = regexp.MustCompile(`(?i)(csrf|xsrf|token|nonce|authenticity|requestverification|captcha)`)

// looksLikeTokenName reports whether a field name carries a token convention.
func looksLikeTokenName(name string) bool {
	return tokenNamePattern.MatchString(name)
}

// formsProbeJS reads every form and every form-less control in one evaluation.
// A field's value is inspected only to derive has_value and, for hidden inputs,
// a boolean token signal; the value itself never leaves the page.
const formsProbeJS = `(maxForms, maxFields, maxOptions, maxSubmits) => {` + cssPathJS + `
  const norm = (s) => (s || '').replace(/\s+/g, ' ').trim();
  const tokenName = /csrf|xsrf|token|nonce|authenticity|requestverification|captcha/i;
  const tokenValue = /^[A-Za-z0-9_.~+\/=:-]{16,}$/;
  const abs = (u) => { if (!u) return ''; try { return new URL(u, location.href).href } catch (e) { return u } };
  const isSubmit = (el) => {
    const tag = el.tagName.toLowerCase();
    const type = (el.getAttribute('type') || (tag === 'button' ? 'submit' : '')).toLowerCase();
    return type === 'submit' || type === 'image';
  };
  const describe = (el) => {
    const tag = el.tagName.toLowerCase();
    const type = tag === 'input' ? (el.getAttribute('type') || 'text').toLowerCase() : tag;
    let label = '';
    if (el.labels && el.labels.length) label = norm(el.labels[0].textContent);
    if (!label) label = norm(el.getAttribute('aria-label'));
    if (!label) label = norm(el.getAttribute('placeholder'));
    if (!label) label = norm(el.getAttribute('title'));
    const name = el.getAttribute('name') || '';
    const hasValue = (type === 'checkbox' || type === 'radio') ? !!el.checked : !!(el.value && String(el.value).length > 0);
    let options = [];
    if (tag === 'select') {
      options = [...el.options].slice(0, maxOptions).map((o) => ({ value: o.value, label: norm(o.textContent) }));
    }
    const hidden = type === 'hidden';
    const valueToken = hidden && tokenValue.test(el.value || '');
    return {
      name: name,
      type: type,
      label: label,
      required: !!el.required || el.getAttribute('aria-required') === 'true',
      has_value: hasValue,
      hidden: hidden,
      token: tokenName.test(name) || valueToken,
      options: options,
      selector: cssPath(el)
    };
  };
  const submit = (el) => ({
    name: el.getAttribute('name') || '',
    type: (el.getAttribute('type') || 'submit').toLowerCase(),
    label: norm(el.getAttribute('value') || el.textContent || ''),
    selector: cssPath(el)
  });
  const fieldsOf = (root) => [...root.querySelectorAll('input, select, textarea')].filter((el) => !isSubmit(el));
  const submitsOf = (root) => [...root.querySelectorAll('input, button')].filter(isSubmit);
  const forms = [...document.querySelectorAll('form')];
  const out = forms.slice(0, maxForms).map((f) => {
    const fields = fieldsOf(f);
    const submits = submitsOf(f);
    return {
      action: abs(f.getAttribute('action')),
      method: (f.getAttribute('method') || 'get').toLowerCase(),
      enctype: (f.getAttribute('enctype') || '').toLowerCase(),
      field_total: fields.length,
      fields: fields.slice(0, maxFields).map(describe),
      submits: submits.slice(0, maxSubmits).map(submit)
    };
  });
  const orphans = [...document.querySelectorAll('input, select, textarea')].filter((el) => !el.closest('form') && !isSubmit(el));
  if (orphans.length) {
    out.push({
      action: '',
      method: '',
      enctype: '',
      field_total: orphans.length,
      fields: orphans.slice(0, maxFields).map(describe),
      submits: []
    });
  }
  return { forms: out, form_total: forms.length + (orphans.length ? 1 : 0) };
}`

// FormOption is one selectable option of a select field.
type FormOption struct {
	// Value is the option's value attribute.
	Value string `json:"value"`
	// Label is the option's visible text.
	Label string `json:"label"`
}

// FormField is one control of a form or a form-less control. It never carries
// the control's current value, only whether one is present.
type FormField struct {
	// Name is the control's name attribute, or "" when unnamed.
	Name string `json:"name"`
	// Type is the input type (lowercased), or "select"/"textarea".
	Type string `json:"type"`
	// Label is the associated label text, an aria-label, a placeholder or a
	// title, in that order.
	Label string `json:"label"`
	// Required is true for required or aria-required controls.
	Required bool `json:"required"`
	// HasValue reports whether the control currently holds a value or is
	// checked. The value itself is never emitted.
	HasValue bool `json:"has_value"`
	// Options are a select field's options; [] for other control types.
	Options []FormOption `json:"options"`
	// Selector is a CSS selector addressing the control.
	Selector string `json:"selector"`
}

// FormSubmit is one submit control of a form.
type FormSubmit struct {
	// Name is the control's name attribute, or "".
	Name string `json:"name"`
	// Type is submit, image or button.
	Type string `json:"type"`
	// Label is the control's visible label or value.
	Label string `json:"label"`
	// Selector is a CSS selector addressing the control.
	Selector string `json:"selector"`
}

// CSRFCandidate is a hidden input whose name or value looks like a CSRF or
// anti-automation token. The value itself is never emitted.
type CSRFCandidate struct {
	// Name is the hidden input's name attribute, or "".
	Name string `json:"name"`
	// Selector is a CSS selector addressing the hidden input.
	Selector string `json:"selector"`
	// Match says what identified the candidate: "name" or "value".
	Match string `json:"match"`
}

// Form is one extracted form. A form with an empty action, method and enctype
// whose fields are all outside a <form> is the synthetic orphan group the probe
// appends for form-less controls.
type Form struct {
	// Index is the 0-based form position in document order.
	Index int `json:"index"`
	// Action is the absolute form action, or "".
	Action string `json:"action"`
	// Method is the form method, lowercased ("get" or "post").
	Method string `json:"method"`
	// Enctype is the form enctype, lowercased, or "" when unset.
	Enctype string `json:"enctype"`
	// Fields are the non-submit controls of the form.
	Fields []FormField `json:"fields"`
	// Submits are the submit controls of the form.
	Submits []FormSubmit `json:"submits"`
	// CSRFCandidates are hidden inputs whose name or value looks like a token.
	CSRFCandidates []CSRFCandidate `json:"csrf_candidates"`
}

// FormsResult is the JSON envelope returned by PageForms.
type FormsResult struct {
	// Forms are the extracted forms in document order.
	Forms []Form `json:"forms"`
	// Count is len(Forms).
	Count int `json:"count"`
	// Truncated is true when any form, field, option, submit or CSRF candidate
	// was dropped by a cap.
	Truncated bool `json:"truncated"`
	// FormsDropped is how many detected forms the form cap omitted.
	FormsDropped int `json:"forms_dropped"`
	// FieldsDropped is how many fields every reported form dropped at the field
	// cap, summed across forms.
	FieldsDropped int `json:"fields_dropped"`
}

// rawForms is the probe's wire shape; it carries the true form total alongside
// the safety-capped collections.
type rawForms struct {
	Forms     []rawForm `json:"forms"`
	FormTotal int       `json:"form_total"`
}

// rawForm is one form as the probe reports it.
type rawForm struct {
	Action     string      `json:"action"`
	Method     string      `json:"method"`
	Enctype    string      `json:"enctype"`
	FieldTotal int         `json:"field_total"`
	Fields     []rawField  `json:"fields"`
	Submits    []rawSubmit `json:"submits"`
}

// rawField is one control as the probe reports it. It deliberately has no value
// field: a value on the wire is ignored here and can never reach the output.
type rawField struct {
	Name     string      `json:"name"`
	Type     string      `json:"type"`
	Label    string      `json:"label"`
	Required bool        `json:"required"`
	HasValue bool        `json:"has_value"`
	Hidden   bool        `json:"hidden"`
	Token    bool        `json:"token"`
	Options  []rawOption `json:"options"`
	Selector string      `json:"selector"`
}

// rawOption is one select option as the probe reports it.
type rawOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// rawSubmit is one submit control as the probe reports it.
type rawSubmit struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Label    string `json:"label"`
	Selector string `json:"selector"`
}

// PageForms returns every form plus the form-less controls as
// {"forms":[{index,action,method,enctype,fields,submits,csrf_candidates}],
// "count":N,"truncated":bool,"forms_dropped":N,"fields_dropped":N}. Fields
// report has_value instead of the value, and csrf_candidates lists hidden inputs
// whose name or value looks like a token. forms_dropped and fields_dropped count
// the forms and fields the caps omitted. The aspect self-caps, so it bypasses
// max_bytes.
func PageForms(ctx context.Context, p *browsers.Page) (string, error) {
	ctx, done, err := beginPage(ctx, p, "forms")
	if err != nil {
		return "", err
	}
	defer done()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	res, err := p.Rod.Context(ctx).Eval(
		formsProbeJS,
		formSafetyForms,
		formSafetyFields,
		formSafetyOptions,
		formSafetySubmits,
	)
	if err != nil {
		return "", fmt.Errorf("evaluate forms: %w", err)
	}

	var raw rawForms
	if err := res.Value.Unmarshal(&raw); err != nil {
		return "", fmt.Errorf("decode forms: %w", err)
	}

	b, err := json.Marshal(shapeForms(raw))
	if err != nil {
		return "", fmt.Errorf("encode forms: %w", err)
	}
	return string(b), nil
}

// shapeForms applies the form caps to the probe's raw collections and builds the
// wire envelope. Hidden inputs whose name or value looks like a token become CSRF
// candidates; no control value is ever copied. The dropped form and field counts
// make the truncation quantitative.
func shapeForms(raw rawForms) FormsResult {
	total := raw.FormTotal
	if total < len(raw.Forms) {
		total = len(raw.Forms)
	}

	forms := raw.Forms
	truncated := false
	formsDropped := 0
	if len(forms) > formMaxForms {
		forms = forms[:formMaxForms]
		truncated = true
	}
	if total > len(forms) {
		formsDropped = total - len(forms)
		truncated = true
	}

	out := FormsResult{
		Forms:         make([]Form, 0, len(forms)),
		FormsDropped:  formsDropped,
		FieldsDropped: 0,
	}
	for i, rf := range forms {
		f := Form{
			Index:          i,
			Action:         rf.Action,
			Method:         rf.Method,
			Enctype:        rf.Enctype,
			Fields:         make([]FormField, 0, len(rf.Fields)),
			Submits:        make([]FormSubmit, 0, len(rf.Submits)),
			CSRFCandidates: make([]CSRFCandidate, 0, len(rf.Fields)),
		}

		fieldTotal := rf.FieldTotal
		if fieldTotal < len(rf.Fields) {
			fieldTotal = len(rf.Fields)
		}
		fields := rf.Fields
		if len(fields) > formMaxFields {
			fields = fields[:formMaxFields]
			truncated = true
		}
		if fieldTotal > len(fields) {
			out.FieldsDropped += fieldTotal - len(fields)
			truncated = true
		}
		for _, r := range fields {
			opts := r.Options
			if len(opts) > formMaxOptions {
				opts = opts[:formMaxOptions]
				truncated = true
			}
			field := FormField{
				Name:     r.Name,
				Type:     r.Type,
				Label:    r.Label,
				Required: r.Required,
				HasValue: r.HasValue,
				Options:  make([]FormOption, 0, len(opts)),
				Selector: r.Selector,
			}
			for _, o := range opts {
				field.Options = append(field.Options, FormOption{Value: o.Value, Label: o.Label})
			}
			f.Fields = append(f.Fields, field)

			if !r.Hidden || (!looksLikeTokenName(r.Name) && !r.Token) {
				continue
			}
			if len(f.CSRFCandidates) >= formMaxCSRF {
				truncated = true
				continue
			}
			match := "value"
			if looksLikeTokenName(r.Name) {
				match = "name"
			}
			f.CSRFCandidates = append(f.CSRFCandidates, CSRFCandidate{
				Name:     r.Name,
				Selector: r.Selector,
				Match:    match,
			})
		}

		submits := rf.Submits
		if len(submits) > formMaxSubmits {
			submits = submits[:formMaxSubmits]
			truncated = true
		}
		for _, s := range submits {
			f.Submits = append(f.Submits, FormSubmit{
				Name:     s.Name,
				Type:     s.Type,
				Label:    s.Label,
				Selector: s.Selector,
			})
		}

		out.Forms = append(out.Forms, f)
	}
	out.Count = len(out.Forms)
	out.Truncated = truncated
	return out
}

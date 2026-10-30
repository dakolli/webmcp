package pageops

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/dakolli/webmcp/pkg/browsers"
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

// fillFormArg is one selector/value or name/value pair handed to the resolve
// script.
type fillFormArg struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// fillFormResolveJS resolves the target form and every field in one evaluation.
// It reuses cssPath, the same selector builder PageForms reports, so a filled
// field's selector matches what=forms. Each unresolved key is skipped with a
// reason; the form is the target selector or the closest form of the first
// resolved field.
const fillFormResolveJS = `(formSelector, fields, values) => {` + cssPathJS + `
  const form = formSelector ? document.querySelector(formSelector) : null;
  const scope = form || document;
  const describe = (el, value) => ({
    selector: cssPath(el),
    name: el.getAttribute('name') || '',
    tag: el.tagName.toLowerCase(),
    type: (el.tagName.toLowerCase() === 'input' ? (el.getAttribute('type') || 'text').toLowerCase() : ''),
    value: value
  });
  const found = [];
  const skipped = [];
  for (const f of fields) {
    let el = null;
    try { el = scope.querySelector(f.key); } catch (e) { el = null; }
    if (!el) { skipped.push({selector: f.key, reason: 'no element matched'}); continue; }
    found.push(describe(el, f.value));
  }
  if (form) {
    for (const v of values) {
      let el = null;
      for (const cand of form.elements) { if (cand.name === v.key) { el = cand; break; } }
      if (!el) { skipped.push({selector: '[name="' + v.key + '"]', reason: 'no field named ' + v.key + ' in the form'}); continue; }
      found.push(describe(el, v.value));
    }
  }
  let resolved = form;
  if (!resolved) {
    for (const f of found) {
      const el = document.querySelector(f.selector);
      const c = el && el.closest ? el.closest('form') : null;
      if (c) { resolved = c; break; }
    }
  }
  return {form: resolved ? cssPath(resolved) : '', found: found, skipped: skipped};
}`

// fillFormApplyJS writes every resolved control through its native setter and
// dispatches the events a framework listens for, then submits the form. It never
// moves the pointer or presses a key.
const fillFormApplyJS = `(payload) => {
  const setNative = (el, prop, val) => {
    let node = el;
    while (node) {
      const desc = Object.getOwnPropertyDescriptor(node, prop);
      if (desc && desc.set) { desc.set.call(el, val); return; }
      node = Object.getPrototypeOf(node);
    }
    el[prop] = val;
  };
  const applied = [];
  const skipped = [];
  for (const w of payload.writes) {
    let el = null;
    try { el = document.querySelector(w.selector); } catch (e) { el = null; }
    if (!el) { skipped.push({selector: w.selector, reason: 'detached before write'}); continue; }
    if (w.kind === 'checked') {
      setNative(el, 'checked', true);
    } else {
      setNative(el, 'value', w.value);
    }
    for (const ev of w.events) el.dispatchEvent(new Event(ev, {bubbles: true}));
    applied.push(w.selector);
  }
  let submitted = false;
  if (payload.submit !== 'none' && payload.form) {
    const form = document.querySelector(payload.form);
    if (form) {
      if (payload.submit === 'click') {
        const btn = form.querySelector('button[type="submit"], input[type="submit"], input[type="image"], button:not([type])');
        if (btn) { btn.click(); submitted = true; }
      } else {
        form.requestSubmit();
        submitted = true;
      }
    }
  }
  return {applied: applied, skipped: skipped, submitted: submitted};
}`

// fieldWriteKind names the DOM operation a fill_form write performs.
type fieldWriteKind string

const (
	// fieldWriteValue sets value on a text-like control and fires input+change.
	fieldWriteValue fieldWriteKind = "value"
	// fieldWriteTextarea sets value on a textarea and fires input.
	fieldWriteTextarea fieldWriteKind = "textarea"
	// fieldWriteSelect sets the option value on a select and fires change.
	fieldWriteSelect fieldWriteKind = "select"
	// fieldWriteChecked checks a checkbox or radio and fires change.
	fieldWriteChecked fieldWriteKind = "checked"
)

// fieldWrite is one resolved control write the page script applies.
type fieldWrite struct {
	Selector string   `json:"selector"`
	Kind     string   `json:"kind"`
	Value    string   `json:"value"`
	Events   []string `json:"events"`
}

// coerceField maps a resolved control's tag and input type to its write kind and
// the events the page dispatches afterwards. Checkbox and radio controls are
// checked; a select takes the option value; a textarea takes the value with an
// input event; every other control takes the value with input and change.
func coerceField(selector, tag, typ, value string) fieldWrite {
	w := fieldWrite{
		Selector: selector,
		Kind:     string(fieldWriteValue),
		Value:    value,
		Events:   []string{"input", "change"},
	}
	switch tag {
	case "select":
		w.Kind = string(fieldWriteSelect)
		w.Events = []string{"change"}
	case "textarea":
		w.Kind = string(fieldWriteTextarea)
		w.Events = []string{"input"}
	case "input":
		if typ == "checkbox" || typ == "radio" {
			w.Kind = string(fieldWriteChecked)
			w.Events = []string{"change"}
			w.Value = ""
		}
	}
	return w
}

// fillFormResolved is one control the resolve script found.
type fillFormResolved struct {
	Selector string `json:"selector"`
	Name     string `json:"name"`
	Tag      string `json:"tag"`
	Type     string `json:"type"`
	Value    string `json:"value"`
}

// fillFormResolve is the resolve script's wire shape.
type fillFormResolve struct {
	Form    string             `json:"form"`
	Found   []fillFormResolved `json:"found"`
	Skipped []fillFormSkip     `json:"skipped"`
}

// fillFormSkip is one field the fill could not write, with the reason.
type fillFormSkip struct {
	Selector string `json:"selector"`
	Reason   string `json:"reason"`
}

// fillFormApplyInput is the apply script's wire shape.
type fillFormApplyInput struct {
	Writes []fieldWrite `json:"writes"`
	Submit string       `json:"submit"`
	Form   string       `json:"form"`
}

// fillFormApply is the apply script's result.
type fillFormApply struct {
	Applied   []string       `json:"applied"`
	Skipped   []fillFormSkip `json:"skipped"`
	Submitted bool           `json:"submitted"`
}

// FillFormFilled is one control PageFillForm wrote.
type FillFormFilled struct {
	// Name is the control's name attribute, or "" when unnamed.
	Name string `json:"name"`
	// Selector is the CSS selector that addressed the control.
	Selector string `json:"selector"`
}

// FillFormSkipped is one field PageFillForm could not resolve or write.
type FillFormSkipped struct {
	// Selector is the selector or name key that did not resolve.
	Selector string `json:"selector"`
	// Reason says why the field was skipped.
	Reason string `json:"reason"`
}

// FillFormResult is the JSON envelope returned by PageFillForm.
type FillFormResult struct {
	// Form is the resolved form selector, or "" when no form was targeted or
	// found.
	Form string `json:"form"`
	// Filled are the controls the write applied.
	Filled []FillFormFilled `json:"filled"`
	// Skipped are the fields that did not resolve or write.
	Skipped []FillFormSkipped `json:"skipped"`
	// Submit is the canonical submit mode: requestSubmit, click or none.
	Submit string `json:"submit"`
	// URLAfter is the page URL after the submit and any wait.
	URLAfter string `json:"url_after"`
}

// fillFormPhase is the locked phase's output, before the post-submit wait and
// URL read.
type fillFormPhase struct {
	Form      string
	Filled    []FillFormFilled
	Skipped   []FillFormSkipped
	Submit    string
	Submitted bool
}

// FillFormMode normalises the submit mode. An empty mode and "requestSubmit"
// both submit through form.requestSubmit(); "click" clicks the form's submit
// control; "none" fills only.
func FillFormMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "requestsubmit", "request_submit":
		return "requestSubmit", nil
	case "click":
		return "click", nil
	case "none":
		return "none", nil
	default:
		return "", fmt.Errorf("fill_form: invalid arguments: unknown submit %q (must be requestSubmit, click or none)", mode)
	}
}

// ValidateFillForm checks the submit mode and the two input shapes, returning
// the canonical submit mode. Exactly one of fields (selector to value) or values
// (name to value) must be supplied, and values requires a form selector.
func ValidateFillForm(selector, submit string, fields, values map[string]string) (string, error) {
	mode, err := FillFormMode(submit)
	if err != nil {
		return "", err
	}
	if len(fields) == 0 && len(values) == 0 {
		return "", fmt.Errorf("fill_form: missing required argument — pass fields (selector to value) or values (name to value)")
	}
	if len(fields) > 0 && len(values) > 0 {
		return "", fmt.Errorf("fill_form: fields and values are mutually exclusive; pass one")
	}
	if len(values) > 0 && selector == "" {
		return "", fmt.Errorf("fill_form: values requires a selector (the form whose fields are resolved by name)")
	}
	return mode, nil
}

// PageFillForm fills a form's fields in one call and optionally submits it. The
// target form is selector, or empty when fields carries full field selectors.
// fields maps a field selector to its value; values maps a field name to its
// value and requires a form selector. Each control is written through its native
// setter and then receives the events React and Vue listen for. submit is
// requestSubmit (default), click or none; waitMode (load, idle or stable) waits
// after the submit. A field that does not resolve is reported in skipped, never
// as an error; a missing form is an element_not_found. The action never moves the
// pointer or presses a key.
func PageFillForm(
	ctx context.Context,
	p *browsers.Page,
	selector string,
	fields, values map[string]string,
	submit, waitMode string,
) (string, error) {
	mode, err := ValidateFillForm(selector, submit, fields, values)
	if err != nil {
		return "", err
	}

	fieldArgs := make([]fillFormArg, 0, len(fields))
	for _, k := range slices.Sorted(maps.Keys(fields)) {
		fieldArgs = append(fieldArgs, fillFormArg{Key: k, Value: fields[k]})
	}
	valueArgs := make([]fillFormArg, 0, len(values))
	for _, k := range slices.Sorted(maps.Keys(values)) {
		valueArgs = append(valueArgs, fillFormArg{Key: k, Value: values[k]})
	}

	before, _ := PageStateIfFree(ctx, p)

	phase, err := browsers.GuardPage(ctx, p, "fill_form", func(ctx context.Context) (fillFormPhase, error) {
		if p.Rod == nil {
			return fillFormPhase{}, fmt.Errorf("page session is closed")
		}
		page := p.Rod.Context(ctx)
		if selector != "" {
			if _, err := resolveSelector(page, selector); err != nil {
				return fillFormPhase{}, actionError("fill_form", "", selector, resolveError(selector, err))
			}
		}

		res, err := page.Eval(fillFormResolveJS, selector, fieldArgs, valueArgs)
		if err != nil {
			return fillFormPhase{}, fmt.Errorf("fill_form: resolve fields: %w", err)
		}
		var resolved fillFormResolve
		if err := res.Value.Unmarshal(&resolved); err != nil {
			return fillFormPhase{}, fmt.Errorf("fill_form: decode fields: %w", err)
		}

		if mode != "none" && resolved.Form == "" {
			return fillFormPhase{}, fmt.Errorf("fill_form: invalid arguments: submit %q requires a form (pass a form selector or a field inside a <form>)", mode)
		}

		writes := make([]fieldWrite, 0, len(resolved.Found))
		filled := make([]FillFormFilled, 0, len(resolved.Found))
		skipped := make([]FillFormSkipped, 0, len(resolved.Skipped)+len(resolved.Found))
		for _, f := range resolved.Found {
			writes = append(writes, coerceField(f.Selector, f.Tag, f.Type, f.Value))
			filled = append(filled, FillFormFilled{Name: f.Name, Selector: f.Selector})
		}
		for _, s := range resolved.Skipped {
			skipped = append(skipped, FillFormSkipped{Selector: s.Selector, Reason: s.Reason})
		}

		payload := fillFormApplyInput{Writes: writes, Submit: mode, Form: resolved.Form}
		res, err = page.Eval(fillFormApplyJS, payload)
		if err != nil {
			return fillFormPhase{}, fmt.Errorf("fill_form: write fields: %w", err)
		}
		var applied fillFormApply
		if err := res.Value.Unmarshal(&applied); err != nil {
			return fillFormPhase{}, fmt.Errorf("fill_form: decode write result: %w", err)
		}
		for _, s := range applied.Skipped {
			skipped = append(skipped, FillFormSkipped{Selector: s.Selector, Reason: s.Reason})
		}

		return fillFormPhase{
			Form:      resolved.Form,
			Filled:    filled,
			Skipped:   skipped,
			Submit:    mode,
			Submitted: applied.Submitted,
		}, nil
	})
	if err != nil {
		return "", err
	}

	urlAfter := ""
	if phase.Submitted {
		switch waitMode {
		case "load", "idle", "stable":
			if err := WaitForPage(ctx, p, waitMode); err != nil {
				return "", fmt.Errorf("fill_form: wait after submit: %w", err)
			}
		}
		after := settleAfterAction(ctx, func() (PageState, error) {
			return PageStateIfFree(ctx, p)
		}, "fill_form", before, p.RawActions(ctx))
		urlAfter = after.URL
	} else if st, err := PageStateIfFree(ctx, p); err == nil {
		urlAfter = st.URL
	}

	result := FillFormResult{
		Form:     phase.Form,
		Filled:   phase.Filled,
		Skipped:  phase.Skipped,
		Submit:   phase.Submit,
		URLAfter: urlAfter,
	}
	b, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("fill_form: encode result: %w", err)
	}
	return string(b), nil
}

package browsers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// MaxMarks caps the numbered boxes drawn for one marks capture. The overlay
// script applies the same cap; the engine trims any excess and reports the
// remainder via MarksResult.Truncated.
const MaxMarks = 60

// Mark is one numbered set-of-marks box: the label drawn over the target, its
// role and clipped text, and the target's viewport CSS-px geometry.
type Mark struct {
	// Index is the number drawn in the box's label, starting at 1.
	Index int `json:"index"`
	// Role is the element's ARIA role attribute, else its lowercased tag.
	Role string `json:"role"`
	// Text is the aria-label → title → placeholder → innerText fallback,
	// whitespace-normalized and capped at 60 chars. Omitted when empty.
	Text string `json:"text,omitempty"`
	// X is the box's left edge in viewport CSS px, usable directly with
	// page_act click_at.
	X float64 `json:"x"`
	// Y is the box's top edge in viewport CSS px, usable directly with
	// page_act click_at.
	Y float64 `json:"y"`
	// W is the box's width in viewport CSS px.
	W float64 `json:"width"`
	// H is the box's height in viewport CSS px.
	H float64 `json:"height"`
}

// MarksResult is the wire shape of what=marks: the numbered mark table, the
// annotated viewport screenshot and the viewport the boxes were measured in.
type MarksResult struct {
	// Marks are the drawn boxes in document order, never nil on the wire.
	Marks []Mark `json:"marks"`
	// Screenshot is the annotated viewport PNG as a base64 data-URI.
	Screenshot string `json:"screenshot"`
	// ViewportWidth is the CSS-px viewport width at capture time.
	ViewportWidth int `json:"viewport_width"`
	// ViewportHeight is the CSS-px viewport height at capture time.
	ViewportHeight int `json:"viewport_height"`
	// Truncated is the count of candidates beyond MaxMarks (0 when none).
	Truncated int `json:"truncated"`
}

// marksSnapshot is the raw JSON the overlay script returns: the capped mark
// table, the viewport it measured and the candidate count beyond the cap.
type marksSnapshot struct {
	// Marks is the capped mark table.
	Marks []Mark `json:"marks"`
	// Width is window.innerWidth at capture time.
	Width int `json:"width"`
	// Height is window.innerHeight at capture time.
	Height int `json:"height"`
	// Truncated is the count of candidates beyond the cap.
	Truncated int `json:"truncated"`
}

// Marks draws numbered boxes over every visible interactive element, captures
// the annotated viewport and removes the overlay again, returning the mark
// table plus the screenshot.
//
// The method takes the page lock itself (mirroring Page.A11yNodes): callers
// must NOT pre-lock, and the nil-Rod guard runs under the lock. Exactly three
// rod calls run under that one lock — install overlay, screenshot, remove
// overlay — and the removal always runs after the install, including when the
// screenshot or the decode fails, so no data-wm-marks node is left behind.
// Coordinates are getBoundingClientRect values in viewport CSS px, which an
// agent can pass straight to click_at.
func (p *Page) Marks(ctx context.Context) (out MarksResult, err error) {
	if lockErr := p.lockRead(ctx, "marks"); lockErr != nil {
		return MarksResult{}, lockErr
	}
	defer p.Unlock()
	if p.Rod == nil {
		return MarksResult{}, fmt.Errorf("page session is closed")
	}

	page := p.Rod.Context(ctx)
	obj, err := page.Eval(marksInstallJS)
	if err != nil {
		return MarksResult{}, fmt.Errorf("capture marks: %w", err)
	}
	defer func() {
		if _, rmErr := page.Eval(marksRemoveJS); rmErr != nil && err == nil {
			err = fmt.Errorf("capture marks: %w", rmErr)
		}
	}()

	png, err := page.Screenshot(false, nil)
	if err != nil {
		return MarksResult{}, fmt.Errorf("capture marks: %w", err)
	}

	var snap marksSnapshot
	if err = json.Unmarshal([]byte(obj.Value.Str()), &snap); err != nil {
		return MarksResult{}, fmt.Errorf("capture marks: %w", err)
	}
	marks := snap.Marks
	if len(marks) > MaxMarks {
		snap.Truncated += len(marks) - MaxMarks
		marks = marks[:MaxMarks]
	}
	if marks == nil {
		marks = []Mark{}
	}
	return MarksResult{
		Marks:          marks,
		Screenshot:     "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
		ViewportWidth:  snap.Width,
		ViewportHeight: snap.Height,
		Truncated:      snap.Truncated,
	}, nil
}

// marksInstallJS collects the visible interactive candidates, appends one
// fixed overlay container holding a numbered box per mark, and returns the
// mark table with the viewport size as a JSON string. It drops any stale
// overlay first, keeps the outermost of nested candidates, and caps the table
// at MaxMarks. Boxes are built with DOM nodes, never HTML strings, so page
// text cannot become markup.
const marksInstallJS = `() => {
  const cap = 60;
  const sel = 'a[href], button, input, select, textarea, [role=button], [role=link], [role=checkbox], [role=tab], [role=menuitem], [contenteditable="true"], [tabindex]:not([tabindex="-1"])';
  for (const old of document.querySelectorAll('[data-wm-marks]')) old.remove();
  const norm = (s) => (s || '').replace(/\s+/g, ' ').trim().slice(0, 60);
  const labelOf = (el) => {
    for (const k of ['aria-label', 'title', 'placeholder']) {
      const v = el.getAttribute(k);
      if (v && v.trim()) return norm(v);
    }
    return norm(el.innerText || '');
  };
  const round = (v) => Math.round(v * 10) / 10;
  const overlay = document.createElement('div');
  overlay.setAttribute('data-wm-marks', '1');
  overlay.style.cssText = 'position:fixed;inset:0;pointer-events:none;z-index:2147483647';
  const marks = [];
  const accepted = new Set();
  let truncated = 0;
  for (const el of document.querySelectorAll(sel)) {
    const tag = el.tagName.toLowerCase();
    if (el.disabled || el.getAttribute('aria-hidden') === 'true') continue;
    if (tag === 'input' && el.getAttribute('type') === 'hidden') continue;
    if (el.closest('[data-wm-marks]')) continue;
    const cs = getComputedStyle(el);
    if (cs.display === 'none' || cs.visibility === 'hidden' || parseFloat(cs.opacity) === 0) continue;
    const r = el.getBoundingClientRect();
    if (r.width <= 0 || r.height <= 0) continue;
    if (r.bottom < 0 || r.top > window.innerHeight || r.right < 0 || r.left > window.innerWidth) continue;
    let nested = false;
    for (let a = el.parentElement; a; a = a.parentElement) {
      if (accepted.has(a)) { nested = true; break; }
    }
    if (nested) continue;
    if (marks.length >= cap) { accepted.add(el); truncated++; continue; }
    accepted.add(el);
    const x = round(r.left), y = round(r.top), w = round(r.width), h = round(r.height);
    const box = document.createElement('div');
    box.style.cssText = 'position:fixed;left:' + x + 'px;top:' + y + 'px;width:' + w + 'px;height:' + h + 'px;border:2px solid #ff2d95;background:rgba(255,45,149,0.12);box-sizing:border-box';
    const badge = document.createElement('b');
    badge.textContent = String(marks.length + 1);
    badge.style.cssText = 'position:absolute;left:0;top:-14px;font:700 11px/14px monospace;color:#fff;background:#ff2d95;padding:0 3px';
    box.appendChild(badge);
    overlay.appendChild(box);
    marks.push({ index: marks.length + 1, role: el.getAttribute('role') || tag, text: labelOf(el), x: x, y: y, width: w, height: h });
  }
  document.documentElement.appendChild(overlay);
  return JSON.stringify({ marks: marks, width: window.innerWidth, height: window.innerHeight, truncated: truncated });
}`

// marksRemoveJS deletes every overlay container so the page DOM is left
// exactly as it was found.
const marksRemoveJS = `() => {
  for (const n of document.querySelectorAll('[data-wm-marks]')) n.remove();
}`

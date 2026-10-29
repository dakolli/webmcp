package browsers

import (
	"fmt"
	"strings"

	"github.com/ysmood/gson"
)

const (
	// MutationRingCap bounds the per-page DOM mutation timeline. One entry is one
	// MutationObserver callback (a batch of records), so a page that rewrites a
	// large subtree cannot flood the ring. Older batches are evicted and counted
	// as dropped.
	MutationRingCap = 500
	// maxMutationTargetRunes caps one entry's target path.
	maxMutationTargetRunes = 120
	// maxMutationTextRunes caps one characterData entry's new text.
	maxMutationTextRunes = 120
	// mutationBindingName is the page-side global the observer reports to.
	mutationBindingName = "__wmMutationPush"
)

// MutationEntry is one recorded DOM mutation batch: the kind of change, the
// short CSS path of the node it touched, the changed attribute (attributes
// only), how many nodes the batch added and removed, and the new text
// (characterData only, clipped at 120 runes).
type MutationEntry struct {
	// Seq is the page-monotonic sequence number. It never resets, even across a
	// full navigation.
	Seq int64 `json:"seq"`
	// TS is the page-side batch time in Unix milliseconds.
	TS int64 `json:"ts"`
	// Kind is "attributes", "characterData" or "childList".
	Kind string `json:"kind"`
	// Target is a short CSS-like path to the mutated node, capped at 120 runes.
	Target string `json:"target"`
	// Attribute names the changed attribute, empty for other kinds.
	Attribute string `json:"attribute"`
	// Added counts the nodes a childList batch added, never the nodes.
	Added int `json:"added"`
	// Removed counts the nodes a childList batch removed, never the nodes.
	Removed int `json:"removed"`
	// Text is the new characterData text, capped at 120 runes.
	Text string `json:"text"`
}

// Mutations returns the recorded batches with a sequence above since, oldest
// first, plus the ring's dropped count and the newest sequence on the page.
// selector keeps only entries whose target contains it; empty keeps every
// entry. The read touches only the observer's own buffer, so it never takes the
// page lock or makes a CDP call.
func (p *Page) Mutations(since int64, selector string) ([]MutationEntry, int, int64) {
	o := p.observer
	if o == nil {
		return []MutationEntry{}, 0, 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return filterMutations(o.mut.take(), since, selector), int(o.mut.dropped), o.mutSeq
}

// recordMutation stamps a page-reported batch with the next page sequence and
// buffers it. The entry is clipped before it is stored, so a page that calls the
// binding directly cannot make one entry dominate the ring.
func (o *pageObserver) recordMutation(e MutationEntry) {
	e = clipMutationEntry(e)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.mutSeq++
	e.Seq = o.mutSeq
	o.mut.push(e)
}

// clipMutationEntry caps the target path and text at their rune limits.
func clipMutationEntry(e MutationEntry) MutationEntry {
	e.Target, _ = clipRunes(e.Target, maxMutationTargetRunes)
	e.Text, _ = clipRunes(e.Text, maxMutationTextRunes)
	return e
}

// filterMutations keeps entries newer than since, narrowed to targets that
// contain selector when one is given.
func filterMutations(
	entries []MutationEntry,
	since int64,
	selector string,
) []MutationEntry {
	out := make([]MutationEntry, 0, len(entries))
	for _, e := range entries {
		if e.Seq <= since {
			continue
		}
		if selector != "" && !strings.Contains(e.Target, selector) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// decodeMutation reads one page-reported batch from the parsed binding argument.
// The exposed Go function receives a value rod already parsed, so re-unmarshaling
// it returns "gson: value has been parsed"; every field is read off the value
// instead. A batch with no kind is still recorded, with kind "unknown" and the
// raw argument's byte length, so a malformed report is visible rather than
// silently dropped.
func decodeMutation(g gson.JSON) MutationEntry {
	kind := mutationField(g, "kind")
	if kind == "" {
		return MutationEntry{
			Kind:   "unknown",
			Target: mutationField(g, "target"),
			Text:   fmt.Sprintf("%d bytes", len(g.JSON("", ""))),
		}
	}
	return MutationEntry{
		Kind:      kind,
		Target:    mutationField(g, "target"),
		Attribute: mutationField(g, "attribute"),
		Added:     g.Get("added").Int(),
		Removed:   g.Get("removed").Int(),
		Text:      mutationField(g, "text"),
		TS:        int64(g.Get("ts").Int()),
	}
}

// mutationField reads one string field from the parsed binding argument,
// returning "" when the field is absent or null.
func mutationField(g gson.JSON, name string) string {
	v := g.Get(name)
	if v.Nil() {
		return ""
	}
	return v.Str()
}

// startMutationObservation arms the page's DOM mutation timeline. It binds the
// page-side reporter first, then registers the observer for every later
// document, so the reporter global exists before the observer's init script
// runs. The entry sequence lives on the page observer, so it keeps increasing
// across a full navigation while the ring survives with it. Capture is
// best-effort: a page that cannot be armed simply reports an empty timeline.
// It runs in the pre-registration window, so no page lock is needed.
func (p *Page) startMutationObservation() {
	o := p.observer
	if p.Rod == nil || o == nil {
		return
	}
	stop, err := p.Rod.Expose(
		mutationBindingName,
		func(g gson.JSON) (any, error) {
			o.recordMutation(decodeMutation(g))
			return nil, nil
		},
	)
	if err != nil {
		return
	}
	remove, err := p.Rod.EvalOnNewDocument(mutationNewDocumentScript)
	if err != nil {
		_ = stop()
		return
	}
	o.mutRemove = remove
	o.mutStop = stop
	_, _ = p.Rod.Eval(mutationInstallJS)
}

// stopMutations unarms the mutation timeline. The page teardown already holds
// the page lock; both calls are best-effort because a closing target may no
// longer answer.
func (o *pageObserver) stopMutations() {
	if o.mutRemove != nil {
		_ = o.mutRemove()
		o.mutRemove = nil
	}
	if o.mutStop != nil {
		_ = o.mutStop()
		o.mutStop = nil
	}
}

// mutationScriptTemplate is the in-page observer. One MutationObserver watches
// the whole document from document_start and reports one batch per callback:
// the first record names the kind and target, added/removed are summed, and the
// last characterData text is kept. Batching by callback keeps a large subtree
// rewrite to one ring entry.
const mutationScriptTemplate = `
if (window.__wmMutationObserved) return;
if (typeof MutationObserver !== 'function') return;
window.__wmMutationObserved = true;
const WM_MUT_TEXT_MAX = %d;

const wmMutClip = (raw, max) => {
  let s;
  try { s = String(raw); } catch (e) { s = ''; }
  if (s.length <= max) return s;
  let v = s.slice(0, max);
  const c = v.charCodeAt(v.length - 1);
  if (c >= 0xD800 && c <= 0xDBFF) v = v.slice(0, -1);
  return v + '\u2026';
};

const wmMutPath = (node) => {
  try {
    if (!node) return '';
    let el = node.nodeType === 1 ? node : node.parentElement;
    if (!el) return '';
    if (el.id) return '#' + el.id;
    const parts = [];
    let cur = el;
    while (cur && cur.nodeType === 1 && parts.length < 4) {
      if (cur.id) { parts.unshift('#' + cur.id); break; }
      let part = cur.localName || 'node';
      const cls = typeof cur.className === 'string' ? cur.className.trim().split(/\s+/)[0] : '';
      if (cls && /^[A-Za-z_-][A-Za-z0-9_-]*$/.test(cls)) part += '.' + cls;
      const parent = cur.parentElement;
      if (parent) {
        const sibs = parent.children;
        let total = 0;
        let index = 0;
        for (let i = 0; i < sibs.length; i++) {
          if (sibs[i].localName === cur.localName) {
            total++;
            if (sibs[i] === cur) index = total;
          }
        }
        if (total > 1) part += ':nth-of-type(' + index + ')';
      }
      parts.unshift(part);
      cur = parent;
    }
    return parts.join(' > ');
  } catch (e) {
    return '';
  }
};

const wmMutBatch = (records) => {
  if (!records || records.length === 0) return null;
  const first = records[0];
  let attribute = '';
  let added = 0;
  let removed = 0;
  let text = '';
  for (let i = 0; i < records.length; i++) {
    const m = records[i];
    if (m.addedNodes) added += m.addedNodes.length;
    if (m.removedNodes) removed += m.removedNodes.length;
    if (first.type === 'characterData' && m.type === 'characterData' && m.target) {
      const t = m.target.data || '';
      if (t) text = t;
    }
    if (first.type === 'attributes' && !attribute && m.type === 'attributes' && m.attributeName) {
      attribute = m.attributeName;
    }
  }
  return {
    kind: first.type,
    target: wmMutPath(first.target),
    attribute: attribute,
    added: added,
    removed: removed,
    text: wmMutClip(text, WM_MUT_TEXT_MAX),
    ts: Date.now()
  };
};

try {
  const observer = new MutationObserver((records) => {
    try {
      if (typeof window.__wmMutationPush !== 'function') return;
      const batch = wmMutBatch(records);
      if (batch) window.__wmMutationPush(batch);
    } catch (e) {}
  });
  observer.observe(document, {
    childList: true,
    subtree: true,
    attributes: true,
    characterData: true
  });
  window.__wmMutationObserver = observer;
} catch (e) {}
`

// mutationScript is the observer body with the text cap baked in.
var mutationScript = fmt.Sprintf(mutationScriptTemplate, maxMutationTextRunes)

// mutationInstallJS arms the observer in the current document, for a tab that
// was already loaded when the page was wrapped (a popup).
var mutationInstallJS = "() => {" + mutationScript + "}"

// mutationNewDocumentScript re-injects the observer before each new document's
// own scripts. Init scripts run as raw statements, where the body's top-level
// guard return is a syntax error, so the body runs inside a self-invoking
// function.
var mutationNewDocumentScript = "(() => {" + mutationScript + "})()"

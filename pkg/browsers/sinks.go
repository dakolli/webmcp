package browsers

import (
	"context"
	"encoding/json"
	"fmt"
)

const (
	// SinkRingCap bounds the tracer's per-document hit ring. Older hits are
	// evicted and counted as dropped.
	SinkRingCap = 200
	// MaxSinkHits caps how many hits SinkHits emits; older buffered hits are
	// reported through SinksResult.Truncated instead.
	MaxSinkHits = 100
	// maxSinkValueRunes caps one hit's value so a large HTML write cannot
	// dominate the response.
	maxSinkValueRunes = 200
	// maxSinkFieldRunes caps one stack frame or selector string.
	maxSinkFieldRunes = 200
	// maxSinkStackFrames caps the caller frames kept per hit.
	maxSinkStackFrames = 5
)

// SinkHit is one source-to-sink record: the sink that received a value, its
// context, the value, the page-side stack, the receiving node's short CSS path
// and the hit timestamp.
type SinkHit struct {
	// Source is the tagged source whose value was found in Value, e.g.
	// "location.hash" or "message"; "unknown" when no tracked value matched.
	Source string `json:"source"`
	// Sink is the instrumented API that received the value, e.g. "innerHTML",
	// "eval", "setAttribute:href" or "location.assign".
	Sink string `json:"sink"`
	// Context labels the receiving channel: "html", "attr", "url" or "js".
	Context string `json:"context"`
	// Value is the value handed to the sink, clipped at 200 runes.
	Value string `json:"value"`
	// ValueCut reports that Value was clipped.
	ValueCut bool `json:"value_cut,omitempty"`
	// Stack is the page-side caller stack, first 5 frames, each rendered as
	// "function url:line"; instrumentation frames are stripped.
	Stack []string `json:"stack,omitempty"`
	// Selector is a short CSS path to the node that received the value, when
	// the sink has a node.
	Selector string `json:"selector,omitempty"`
	// TS is the page-side hit time in Unix milliseconds.
	TS int64 `json:"ts"`
}

// SinksResult is the wire shape of what=sinks. Installed reports whether the
// tracer is armed for the page, which survives navigation; the ring is per
// document, so a navigation starts a fresh one.
type SinksResult struct {
	// Installed reports whether the tracer is armed and running in the current
	// document.
	Installed bool `json:"installed"`
	// Hits are the newest MaxSinkHits records, oldest first, never nil on the
	// wire.
	Hits []SinkHit `json:"hits"`
	// Dropped is the count of hits evicted by the SinkRingCap ring.
	Dropped int `json:"dropped"`
	// Truncated is the count of buffered hits older than the emitted window.
	Truncated int `json:"truncated"`
}

// sinkSnapshot is the raw JSON one read returns: the whole ring plus its loss
// counter.
type sinkSnapshot struct {
	Hits    []SinkHit `json:"hits"`
	Dropped int       `json:"dropped"`
}

// InstallSinks arms the source-to-sink tracer on the current document and
// registers it for every later document of the page, so the arm survives a
// full navigation and the script re-injects before each new document's own
// scripts; a client-side route change keeps it anyway. The ring restarts per
// document while the arm lives on the Page, so only a page close disarms it.
// The script is registered once per page and its in-page guard makes a
// repeated install a no-op. Instrumentation changes page behavior (wrapped
// setters and constructors stay visible to the page), so it is opt-in and off
// by default.
//
// The method takes the page lock itself (mirroring Page.Marks): callers must
// NOT pre-lock, and the nil-Rod guard runs under the lock.
func (p *Page) InstallSinks(ctx context.Context) error {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return fmt.Errorf("page session is closed")
	}

	page := p.Rod.Context(ctx)
	if !p.sinkScriptInstalled {
		if _, err := page.EvalOnNewDocument(sinkNewDocumentScript); err != nil {
			return fmt.Errorf("install sink tracer: %w", err)
		}
		p.sinkScriptInstalled = true
	}
	if _, err := page.Eval(sinkInstallJS); err != nil {
		return fmt.Errorf("install sink tracer: %w", err)
	}
	return nil
}

// ClearSinks empties the tracer's hit ring and resets its dropped counter,
// returning how many hits were cleared. It is a no-op on a page where the
// tracer was never installed. The method takes the page lock itself.
func (p *Page) ClearSinks(ctx context.Context) (int, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return 0, fmt.Errorf("page session is closed")
	}

	res, err := p.Rod.Context(ctx).Eval(sinkClearJS)
	if err != nil {
		return 0, fmt.Errorf("clear sink tracer: %w", err)
	}
	raw := res.Value.Str()
	if raw == "null" || raw == "" {
		return 0, nil
	}
	var cleared struct {
		Cleared int `json:"cleared"`
	}
	if err := json.Unmarshal([]byte(raw), &cleared); err != nil {
		return 0, fmt.Errorf("clear sink tracer: %w", err)
	}
	return cleared.Cleared, nil
}

// SinkHits reads the tracer's ring from the current document and returns the
// newest MaxSinkHits records with the ring's dropped and truncated counts. An
// uninstalled page returns Installed false and an empty hit list rather than an
// error. The method takes the page lock itself.
func (p *Page) SinkHits(ctx context.Context) (SinksResult, error) {
	if err := p.lockRead(ctx, "sinks"); err != nil {
		return SinksResult{}, err
	}
	defer p.Unlock()
	if p.Rod == nil {
		return SinksResult{}, fmt.Errorf("page session is closed")
	}

	res, err := p.Rod.Context(ctx).Eval(sinkReadJS)
	if err != nil {
		return SinksResult{}, fmt.Errorf("read sink tracer: %w", err)
	}
	return parseSinkSnapshot(res.Value.Str())
}

// parseSinkSnapshot decodes one read payload. The payload is the JSON string
// "null" when the tracer is not installed in the current document.
func parseSinkSnapshot(raw string) (SinksResult, error) {
	if raw == "null" || raw == "" {
		return SinksResult{Hits: []SinkHit{}}, nil
	}
	var snap *sinkSnapshot
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		return SinksResult{}, fmt.Errorf("decode sink hits: %w", err)
	}
	if snap == nil {
		return SinksResult{Hits: []SinkHit{}}, nil
	}
	return trimSinkHits(snap.Hits, snap.Dropped), nil
}

// trimSinkHits keeps the newest MaxSinkHits records, reports the older buffered
// ones as truncated and re-clips every value at maxSinkValueRunes, so a
// tampered page cannot smuggle an oversized value into the response.
func trimSinkHits(hits []SinkHit, dropped int) SinksResult {
	out := SinksResult{
		Installed: true,
		Hits:      []SinkHit{},
		Dropped:   dropped,
	}
	if len(hits) > MaxSinkHits {
		out.Truncated = len(hits) - MaxSinkHits
		hits = hits[len(hits)-MaxSinkHits:]
	}
	for _, h := range hits {
		h.Value, h.ValueCut = clipSinkValue(h.Value, h.ValueCut)
		h.Selector, _ = clipRunes(h.Selector, maxSinkFieldRunes)
		if len(h.Stack) > maxSinkStackFrames {
			h.Stack = h.Stack[:maxSinkStackFrames]
		}
		for i := range h.Stack {
			h.Stack[i], _ = clipRunes(h.Stack[i], maxSinkFieldRunes)
		}
		out.Hits = append(out.Hits, h)
	}
	return out
}

// clipSinkValue caps s at maxSinkValueRunes runes, keeping an existing cut flag
// and reporting a new cut.
func clipSinkValue(s string, cut bool) (string, bool) {
	clipped, wasCut := clipRunes(s, maxSinkValueRunes)
	if wasCut {
		return clipped, true
	}
	return s, cut
}

// clipRunes caps s at max runes on a rune boundary, appending an ellipsis when
// it cuts.
func clipRunes(s string, max int) (string, bool) {
	r := []rune(s)
	if len(r) <= max {
		return s, false
	}
	return string(r[:max]) + ellipsis, true
}

// sinkScriptTemplate is the tracer body: it tags source reads and wraps every
// sink so each call lands in a bounded per-document ring. It is injected as
// statements before each document's own scripts and once into the current
// document; the installed marker makes a second run a no-op. Every wrapper
// guards its own recording, never its original operation, so the page behaves
// as before. The eval wrapper necessarily turns direct eval into indirect
// (global) eval. Secret sources keep their full value only for in-page
// attribution and are masked in the recorded hit value, so a cookie never
// reaches the wire. The ring and value caps are filled from the Go constants
// so the page and the envelope cannot drift.
const sinkScriptTemplate = `
if (window.__wmSinkInstalled) return;
const WM_CAP = %d;
const WM_VALUE_MAX = %d;
const WM_SOURCE_CAP = 24;
const WM_SOURCE_MIN = 4;
const WM_STACK_MAX = 5;
const WM_RING = { hits: [], dropped: 0 };
window.__wmSinkRing = WM_RING;
window.__wmSinkInstalled = true;

const wmClip = (raw) => {
  const s = String(raw);
  if (s.length <= WM_VALUE_MAX) return { v: s, cut: false };
  let v = s.slice(0, WM_VALUE_MAX);
  const c = v.charCodeAt(v.length - 1);
  if (c >= 0xD800 && c <= 0xDBFF) v = v.slice(0, -1);
  return { v: v, cut: true };
};

const wmSources = [];
const wmClipCookie = (raw) => {
  const parts = String(raw).split(';');
  const out = [];
  for (let i = 0; i < parts.length; i++) {
    const part = parts[i].trim();
    if (!part) continue;
    const eq = part.indexOf('=');
    if (eq < 0) { out.push(part); continue; }
    out.push(part.slice(0, eq).trim() + '=' + part.slice(eq + 1).trim().slice(0, 8) + '…');
  }
  return out.join('; ');
};
const wmTag = (name, value, secret) => {
  if (typeof value !== 'string') return value;
  const v = value.length > 2000 ? value.slice(0, 2000) : value;
  if (v.length < WM_SOURCE_MIN) return value;
  for (let i = 0; i < wmSources.length; i++) {
    if (wmSources[i].name === name && (wmSources[i].match || wmSources[i].value) === v) return value;
  }
  const entry = { name: name, value: v };
  if (secret) {
    entry.value = wmClipCookie(v);
    entry.match = v.slice(0, WM_VALUE_MAX);
    entry.secret = true;
  }
  wmSources.push(entry);
  if (wmSources.length > WM_SOURCE_CAP) wmSources.shift();
  return value;
};

const wmScrub = (s) => {
  let out = s;
  for (let i = wmSources.length - 1; i >= 0; i--) {
    const src = wmSources[i];
    if (!src.secret || !src.match || src.match === src.value) continue;
    if (out.indexOf(src.match) >= 0) out = out.split(src.match).join(src.value);
  }
  return out;
};

const wmLiveLocation = (s) => {
  let best = '';
  let score = 0;
  const consider = (name, v) => {
    if (typeof v !== 'string' || v.length < WM_SOURCE_MIN) return;
    if (s.indexOf(v) >= 0 && v.length > score) { best = name; score = v.length; }
  };
  try {
    for (let i = wmSources.length - 1; i >= 0; i--) {
      if (wmSources[i].name.indexOf('location.') === 0) consider(wmSources[i].name, wmSources[i].value);
    }
    consider('location.hash', location.hash);
    consider('location.search', location.search);
    consider('location.href', location.href);
  } catch (e) {}
  return best;
};

const wmSourceOf = (value) => {
  const s = String(value);
  if (s.length < WM_SOURCE_MIN) return 'unknown';
  for (let i = wmSources.length - 1; i >= 0; i--) {
    const src = wmSources[i];
    if (src.name.indexOf('location.') === 0) continue;
    const v = src.match || src.value;
    if (s.indexOf(v) >= 0) return src.name;
    if (s.length >= 6 && v.indexOf(s) >= 0) return src.name;
  }
  return wmLiveLocation(s) || 'unknown';
};

const __wmStack = () => {
  const out = [];
  try {
    const lines = String(new Error().stack || '').split('\n');
    for (let i = 0; i < lines.length && out.length < WM_STACK_MAX; i++) {
      const line = lines[i].trim();
      if (!line || line.indexOf('Error') === 0) continue;
      const m = line.match(/^at\s+(?:(.+?)\s+\()?(.+):(\d+):(\d+)\)?$/);
      if (!m) continue;
      const fn = (m[1] || 'anonymous').replace(/^Object\./, '').replace(/^new /, '');
      const url = m[2] || '';
      if (fn.indexOf('__wm') === 0 || url.indexOf('__wm') >= 0 || url.indexOf('web-mcp') >= 0) continue;
      const frame = fn + ' ' + (url || '<anonymous>') + ':' + m[3];
      out.push(frame.length > 200 ? frame.slice(0, 200) : frame);
    }
  } catch (e) {}
  return out;
};

const wmPath = (node) => {
  try {
    if (!node || node.nodeType !== 1) return '';
    if (node.id) return '#' + node.id;
    const parts = [];
    let el = node;
    while (el && el.nodeType === 1 && parts.length < 4) {
      if (el.id) { parts.unshift('#' + el.id); break; }
      let part = el.localName || 'node';
      const cls = typeof el.className === 'string' ? el.className.trim().split(/\s+/)[0] : '';
      if (cls && /^[A-Za-z_-][A-Za-z0-9_-]*$/.test(cls)) part += '.' + cls;
      const parent = el.parentElement;
      if (parent) {
        const sibs = parent.children;
        let total = 0;
        let index = 0;
        for (let i = 0; i < sibs.length; i++) {
          if (sibs[i].localName === el.localName) {
            total++;
            if (sibs[i] === el) index = total;
          }
        }
        if (total > 1) part += ':nth-of-type(' + index + ')';
      }
      parts.unshift(part);
      el = parent;
    }
    return parts.join(' > ');
  } catch (e) {
    return '';
  }
};

const __wmPush = (sink, context, raw, node) => {
  try {
    const clipped = wmClip(raw === undefined || raw === null ? '' : raw);
    const hit = {
      source: wmSourceOf(clipped.v),
      sink: sink,
      context: context,
      value: wmScrub(clipped.v),
      stack: __wmStack(),
      selector: wmPath(node),
      ts: Date.now()
    };
    if (clipped.cut) hit.value_cut = true;
    WM_RING.hits.push(hit);
    if (WM_RING.hits.length > WM_CAP) {
      WM_RING.hits.shift();
      WM_RING.dropped++;
    }
  } catch (e) {}
};

const wmAccessor = (proto, prop, onGet, onSet) => {
  try {
    const d = Object.getOwnPropertyDescriptor(proto, prop);
    if (!d || !d.configurable || (!d.get && !d.set)) return;
    const desc = { configurable: true, enumerable: d.enumerable };
    if (d.get && onGet) {
      desc.get = function __wmGet() {
        const v = d.get.call(this);
        try { onGet.call(this, v); } catch (e) {}
        return v;
      };
    } else if (d.get) {
      desc.get = d.get;
    }
    if (d.set && onSet) {
      desc.set = function __wmSet(v) {
        try { onSet.call(this, v); } catch (e) {}
        return d.set.call(this, v);
      };
    } else if (d.set) {
      desc.set = d.set;
    }
    Object.defineProperty(proto, prop, desc);
  } catch (e) {}
};

const wmMethod = (proto, name, record) => {
  try {
    const d = Object.getOwnPropertyDescriptor(proto, name);
    if (!d || typeof d.value !== 'function' || !d.configurable) return;
    Object.defineProperty(proto, name, {
      configurable: true,
      enumerable: d.enumerable,
      writable: true,
      value: function __wmCall() {
        try { record(arguments, this); } catch (e) {}
        return d.value.apply(this, arguments);
      }
    });
  } catch (e) {}
};

const wmMethodResult = (proto, name, record) => {
  try {
    const d = Object.getOwnPropertyDescriptor(proto, name);
    if (!d || typeof d.value !== 'function' || !d.configurable) return;
    Object.defineProperty(proto, name, {
      configurable: true,
      enumerable: d.enumerable,
      writable: true,
      value: function __wmResultCall() {
        const v = d.value.apply(this, arguments);
        try { record.call(this, v); } catch (e) {}
        return v;
      }
    });
  } catch (e) {}
};

const wmGlobalMethod = (name, record) => {
  try {
    const d = Object.getOwnPropertyDescriptor(window, name);
    const orig = d && typeof d.value === 'function' ? d.value : window[name];
    if (typeof orig !== 'function') return;
    Object.defineProperty(window, name, {
      configurable: true,
      enumerable: d ? d.enumerable : false,
      writable: true,
      value: function __wmGlobalCall() {
        try { record(arguments, this); } catch (e) {}
        return orig.apply(window, arguments);
      }
    });
  } catch (e) {}
};

// Location members are unforgeable own properties, so tag them instead of
// wrapping them.
const wmTagLocation = () => {
  try {
    wmTag('location.hash', location.hash);
    wmTag('location.search', location.search);
    wmTag('location.href', location.href);
  } catch (e) {}
};
wmTagLocation();
try {
  window.addEventListener('hashchange', wmTagLocation);
  window.addEventListener('popstate', wmTagLocation);
} catch (e) {}

wmAccessor(Document.prototype, 'URL', function __wmDocumentURL(v) { wmTag('document.URL', v); });
wmAccessor(Document.prototype, 'referrer', function __wmDocumentReferrer(v) { wmTag('document.referrer', v); });

const wmWrapName = (holder, d) => {
  if (!d) return false;
  if (d.get) {
    Object.defineProperty(holder, 'name', {
      configurable: true,
      enumerable: d.enumerable,
      get: function __wmGetName() { return wmTag('window.name', d.get.call(this)); },
      set: d.set ? function __wmSetName(v) { return d.set.call(this, v); } : undefined
    });
    return true;
  }
  if ('value' in d && d.writable) {
    let current = d.value;
    Object.defineProperty(holder, 'name', {
      configurable: true,
      enumerable: d.enumerable,
      get: function __wmGetName() { return wmTag('window.name', current); },
      set: function __wmSetName(v) { current = String(v); }
    });
    return true;
  }
  return false;
};

try {
  if (!wmWrapName(window, Object.getOwnPropertyDescriptor(window, 'name'))) {
    const proto = window.Window && window.Window.prototype;
    if (proto) wmWrapName(proto, Object.getOwnPropertyDescriptor(proto, 'name'));
  }
} catch (e) {}

try {
  const getItem = Storage.prototype.getItem;
  if (typeof getItem === 'function') {
    Object.defineProperty(Storage.prototype, 'getItem', {
      configurable: true,
      enumerable: false,
      writable: true,
      value: function __wmGetItem(key) {
        const v = getItem.call(this, key);
        try {
          const name = this === window.sessionStorage ? 'sessionStorage.getItem' : 'localStorage.getItem';
          wmTag(name, v);
        } catch (e) {}
        return v;
      }
    });
  }
} catch (e) {}

try {
  if (window.MessageEvent) {
    wmAccessor(window.MessageEvent.prototype, 'data', function __wmMessageData(v) {
      wmTag('message', typeof v === 'string' ? v : JSON.stringify(v));
    });
  }
} catch (e) {}

wmAccessor(Document.prototype, 'cookie', function __wmDocumentCookie(v) {
  wmTag('document.cookie', v, true);
});

try {
  wmAccessor(History.prototype, 'state', function __wmHistoryState(v) {
    wmTag('history.state', typeof v === 'string' ? v : JSON.stringify(v));
  });
} catch (e) {}

try {
  if (window.URLSearchParams) {
    wmMethodResult(URLSearchParams.prototype, 'get', function __wmSearchGet(v) {
      wmTag('URLSearchParams.get', v);
    });
    wmMethodResult(URLSearchParams.prototype, 'getAll', function __wmSearchGetAll(v) {
      if (!Array.isArray(v)) return;
      for (let i = 0; i < v.length; i++) wmTag('URLSearchParams.getAll', v[i]);
    });
  }
} catch (e) {}

wmAccessor(Element.prototype, 'innerHTML', null, function __wmInnerHTMLSet(v) {
  __wmPush('innerHTML', 'html', v, this);
});
wmAccessor(Element.prototype, 'outerHTML', null, function __wmOuterHTMLSet(v) {
  __wmPush('outerHTML', 'html', v, this);
});

wmMethod(Document.prototype, 'write', function __wmDocWrite(args) {
  let s = '';
  for (let i = 0; i < args.length; i++) s += String(args[i]);
  __wmPush('document.write', 'html', s, null);
});
wmMethod(Document.prototype, 'writeln', function __wmDocWriteln(args) {
  let s = '';
  for (let i = 0; i < args.length; i++) s += String(args[i]);
  __wmPush('document.writeln', 'html', s, null);
});

wmGlobalMethod('eval', function __wmEval(args) {
  __wmPush('eval', 'js', args[0], null);
});

try {
  const d = Object.getOwnPropertyDescriptor(window, 'Function');
  const RealFunction = d && typeof d.value === 'function' ? d.value : window.Function;
  if (typeof RealFunction === 'function') {
    const handler = {
      apply: function __wmFnApply(target, thisArg, args) {
        try { __wmPush('Function', 'js', Array.prototype.join.call(args, ','), null); } catch (e) {}
        return Reflect.apply(target, thisArg, args);
      },
      construct: function __wmFnConstruct(target, args, newTarget) {
        try { __wmPush('Function', 'js', Array.prototype.join.call(args, ','), null); } catch (e) {}
        return Reflect.construct(target, args, newTarget);
      }
    };
    Object.defineProperty(window, 'Function', {
      configurable: true,
      enumerable: d ? d.enumerable : false,
      writable: true,
      value: new Proxy(RealFunction, handler)
    });
  }
} catch (e) {}

const wmTimer = (name) => wmGlobalMethod(name, function __wmTimerCode(args) {
  if (typeof args[0] === 'string') __wmPush(name, 'js', args[0], null);
});
wmTimer('setTimeout');
wmTimer('setInterval');

wmMethod(Element.prototype, 'insertAdjacentHTML', function __wmInsertAdjacentHTML(args) {
  __wmPush('insertAdjacentHTML', 'html', args[1], this);
});

wmMethod(Element.prototype, 'setAttribute', function __wmSetAttribute(args) {
  const name = String(args[0] || '').toLowerCase();
  if (name === 'srcdoc' || name === 'href' || name === 'src' || name.indexOf('on') === 0) {
    __wmPush('setAttribute:' + name, 'attr', args[1], this);
  }
});

wmMethod(Location.prototype, 'assign', function __wmAssign(args) {
  __wmPush('location.assign', 'url', args[0], null);
});
wmMethod(Location.prototype, 'replace', function __wmReplace(args) {
  __wmPush('location.replace', 'url', args[0], null);
});
`

// sinkScript is the tracer body with SinkRingCap and maxSinkValueRunes baked
// in.
var sinkScript = fmt.Sprintf(sinkScriptTemplate, SinkRingCap, maxSinkValueRunes)

// sinkInstallJS runs the tracer once in the current document: Page.Eval needs a
// function expression, so the shared body is wrapped and invoked.
var sinkInstallJS = "() => {" + sinkScript + "}"

// sinkNewDocumentScript re-injects the tracer before each new document's own
// scripts. Init scripts run as raw statements, where the body's top-level guard
// return is a syntax error, so the body runs inside a self-invoking function.
var sinkNewDocumentScript = "(() => {" + sinkScript + "})()"

// sinkReadJS returns the whole ring as a JSON string, or "null" when the tracer
// is not installed in the current document.
const sinkReadJS = `() => window.__wmSinkInstalled && window.__wmSinkRing
  ? JSON.stringify({ hits: window.__wmSinkRing.hits, dropped: window.__wmSinkRing.dropped || 0 })
  : 'null'`

// sinkClearJS empties the ring and reports how many hits it held, or "null"
// when the tracer is not installed.
const sinkClearJS = `() => {
  if (!window.__wmSinkInstalled || !window.__wmSinkRing) return 'null';
  const ring = window.__wmSinkRing;
  const cleared = ring.hits.length;
  ring.hits = [];
  ring.dropped = 0;
  return JSON.stringify({ cleared: cleared });
}`

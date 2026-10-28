package browsers

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-rod/rod"
)

const (
	// PostMessageRingCap bounds the per-window postMessage recorder ring. Older
	// messages are evicted and counted as dropped.
	PostMessageRingCap = 100
	// MaxPostMessages caps how many messages PostMessages emits; older buffered
	// messages are reported through PostMessageResult.Truncated.
	MaxPostMessages = 100
	// PostMessageFrameCap bounds how many iframes one install, read, clear or
	// send walks, so a hostile page cannot turn the recorder into unbounded
	// work.
	PostMessageFrameCap = 20
	// maxPostMessageDataRunes caps one message's data so a large payload cannot
	// dominate the response.
	maxPostMessageDataRunes = 500
	// maxPostMessageFieldRunes caps an origin or stack frame.
	maxPostMessageFieldRunes = 200
	// maxPostMessageStackFrames caps the receive-side frames kept per message.
	maxPostMessageStackFrames = 5
)

// PostMessage is one recorded window "message" event.
type PostMessage struct {
	// Origin is the sender's origin as reported by the event.
	Origin string `json:"origin"`
	// SourceKind labels the sender: window (self-post), frame (another window),
	// worker (a MessagePort or worker), or unknown.
	SourceKind string `json:"source_kind"`
	// Data is the message payload clipped at 500 runes.
	Data string `json:"data"`
	// DataType labels how Data was produced: string, json (a serialized
	// object/array), object (an unserializable object), or other.
	DataType string `json:"data_type"`
	// TS is the page-side receive time in Unix milliseconds.
	TS int64 `json:"ts"`
	// Stack is the receive-side caller stack, up to 5 frames.
	Stack []string `json:"stack,omitempty"`
}

// PostMessageFrame reports one window reached by the recorder walk.
type PostMessageFrame struct {
	// URL is the frame's live location.
	URL string `json:"url"`
	// Probed reports whether the recorder was installed and readable in that
	// frame.
	Probed bool `json:"probed"`
}

// PostMessageResult is the wire shape of what=postmessage. Installed reports
// whether the recorder is armed for the page; messages are aggregated across
// the root window and every reachable iframe, newest last.
type PostMessageResult struct {
	// Installed reports whether the recorder is armed in the root document.
	Installed bool `json:"installed"`
	// Messages are the newest MaxPostMessages records, oldest first, never nil
	// on the wire.
	Messages []PostMessage `json:"messages"`
	// Dropped counts messages evicted by the per-window ring cap.
	Dropped int `json:"dropped"`
	// Truncated counts buffered messages older than the emitted window.
	Truncated int `json:"truncated"`
	// Frames reports each window the read reached.
	Frames []PostMessageFrame `json:"frames"`
}

// PostMessageSend is the result of one post_message action: the target origin
// actually used and the window it was delivered to.
type PostMessageSend struct {
	// Origin is the target origin used (default "*").
	Origin string `json:"origin"`
	// Target is "top" or the CSS selector naming the iframe.
	Target string `json:"target"`
}

// postMessageSnapshot is the raw JSON one window read returns.
type postMessageSnapshot struct {
	Messages []PostMessage `json:"messages"`
	Dropped  int           `json:"dropped"`
}

// postMessageFrame is one live iframe promoted to its own rod page plus its URL.
type postMessageFrame struct {
	page *rod.Page
	url  string
}

// InstallPostMessages arms the postMessage recorder on the current document and
// registers it for every later document of the page, so the arm survives a full
// navigation. It installs one capture-phase listener on the root window and on
// every reachable iframe window; identical message events are recorded once per
// window. The method takes the page lock itself: callers must NOT pre-lock.
func (p *Page) InstallPostMessages(ctx context.Context) error {
	p.Lock()
	defer p.Unlock()
	return p.installPostMessagesLocked(ctx)
}

// installPostMessagesLocked arms the recorder while the caller already holds
// the page lock.
func (p *Page) installPostMessagesLocked(ctx context.Context) error {
	if p.Rod == nil {
		return fmt.Errorf("page session is closed")
	}

	page := p.Rod.Context(ctx)
	if !p.postMessageScriptInstalled {
		if _, err := page.EvalOnNewDocument(postMessageNewDocumentScript); err != nil {
			return fmt.Errorf("install postMessage recorder: %w", err)
		}
		p.postMessageScriptInstalled = true
	}
	if _, err := page.Eval(postMessageInstallJS); err != nil {
		return fmt.Errorf("install postMessage recorder: %w", err)
	}
	for _, frame := range postMessageFrames(ctx, page) {
		if _, err := frame.page.Context(ctx).Eval(postMessageInstallJS); err != nil {
			continue
		}
	}
	return nil
}

// ClearPostMessages empties every reachable recorder ring and resets its
// dropped counter, returning how many messages were cleared. It is a no-op on a
// page where the recorder was never installed. The method takes the page lock
// itself.
func (p *Page) ClearPostMessages(ctx context.Context) (int, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return 0, fmt.Errorf("page session is closed")
	}

	page := p.Rod.Context(ctx)
	total, err := clearPostMessageRing(page)
	if err != nil {
		return 0, fmt.Errorf("clear postMessage recorder: %w", err)
	}
	for _, frame := range postMessageFrames(ctx, page) {
		cleared, err := clearPostMessageRing(frame.page.Context(ctx))
		if err != nil {
			continue
		}
		total += cleared
	}
	return total, nil
}

// PostMessages reads the recorder rings from the root window and every
// reachable iframe, returning the newest MaxPostMessages records with the
// aggregate dropped and truncated counts. An uninstalled page returns Installed
// false and an empty message list rather than an error. The method takes the
// page lock itself.
func (p *Page) PostMessages(ctx context.Context) (PostMessageResult, error) {
	if err := p.lockRead(ctx, "postmessage"); err != nil {
		return PostMessageResult{}, err
	}
	defer p.Unlock()
	if p.Rod == nil {
		return PostMessageResult{}, fmt.Errorf("page session is closed")
	}

	page := p.Rod.Context(ctx)
	installed, err := postMessageInstalled(page)
	if err != nil {
		return PostMessageResult{}, fmt.Errorf("read postMessage recorder: %w", err)
	}
	out := PostMessageResult{
		Installed: installed,
		Messages:  []PostMessage{},
		Frames:    []PostMessageFrame{{URL: pageURL(page), Probed: installed}},
	}
	if !installed {
		return out, nil
	}

	messages, dropped, err := readPostMessageRing(page)
	if err != nil {
		return PostMessageResult{}, fmt.Errorf("read postMessage recorder: %w", err)
	}
	for _, frame := range postMessageFrames(ctx, page) {
		probed, err := postMessageInstalled(frame.page.Context(ctx))
		if err != nil {
			probed = false
		}
		out.Frames = append(out.Frames, PostMessageFrame{URL: frame.url, Probed: probed})
		if !probed {
			continue
		}
		frameMessages, frameDropped, err := readPostMessageRing(frame.page.Context(ctx))
		if err != nil {
			continue
		}
		messages = append(messages, frameMessages...)
		dropped += frameDropped
	}
	return trimPostMessages(messages, dropped, out.Frames), nil
}

// SendPostMessage delivers value to the window named by selector using origin
// as the target origin (default "*"). "top", "self" and "parent" are keywords
// for those windows; any other value is a CSS selector for an <iframe> or
// <frame> whose contentWindow alone receives the message. An empty selector
// means "top". It arms the recorder first so the delivered message can be read
// back, then reports the origin and resolved target. A selector that is not a
// frame, or a frame without a contentWindow, returns an error instead of
// falling back to top. The method takes the page lock itself.
func (p *Page) SendPostMessage(
	ctx context.Context,
	value, selector, origin string,
) (PostMessageSend, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return PostMessageSend{}, fmt.Errorf("page session is closed")
	}
	if err := p.installPostMessagesLocked(ctx); err != nil {
		return PostMessageSend{}, err
	}

	origin = strings.TrimSpace(origin)
	if origin == "" {
		origin = "*"
	}
	target := strings.TrimSpace(selector)
	if target == "" {
		target = "top"
	}

	page := p.Rod.Context(ctx)
	if window, ok := postMessageTargetWindow(target); ok {
		if _, err := page.Eval(
			postMessageWindowSendJS(window),
			postMessagePayload(value),
			origin,
		); err != nil {
			return PostMessageSend{}, fmt.Errorf("send postMessage: %w", err)
		}
	} else if err := sendPostMessageToFrame(page, target, value, origin); err != nil {
		return PostMessageSend{}, err
	}
	awaitPostMessageDelivery(ctx)
	return PostMessageSend{Origin: origin, Target: target}, nil
}

// postMessageTargetWindow maps a target keyword to the JS expression of the
// window it names.
func postMessageTargetWindow(target string) (string, bool) {
	switch target {
	case "top":
		return "window.top", true
	case "self":
		return "window.self", true
	case "parent":
		return "window.parent", true
	default:
		return "", false
	}
}

// sendPostMessageToFrame resolves selector to a frame element and delivers
// value to that element's contentWindow only.
func sendPostMessageToFrame(
	page *rod.Page,
	selector, value, origin string,
) error {
	el, err := page.Element(selector)
	if err != nil {
		return fmt.Errorf("post_message target %q: %w", selector, err)
	}
	res, err := el.Eval(
		`(payload, targetOrigin) => {
  const tag = String(this.tagName || '').toUpperCase();
  if (tag !== 'IFRAME' && tag !== 'FRAME') return false;
  const target = this.contentWindow;
  if (!target) return false;
  target.postMessage(payload, targetOrigin);
  return true;
}`,
		postMessagePayload(value),
		origin,
	)
	if err != nil {
		return fmt.Errorf("send postMessage: %w", err)
	}
	if !res.Value.Bool() {
		return postMessageTargetError(selector)
	}
	return nil
}

// postMessageTargetError is the documented error for a selector that names
// something other than a frame, or a frame whose contentWindow is unavailable.
func postMessageTargetError(selector string) error {
	return fmt.Errorf(
		`post_message target %q is not a frame — pass "top", "self", "parent", or a CSS selector for an <iframe> or <frame>`,
		selector,
	)
}

// postMessagePayload decodes a complete JSON object or array so it is delivered
// as that structure; anything else is delivered as a string.
func postMessagePayload(value string) any {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return value
	}
	var payload any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return value
	}
	return payload
}

// postMessageInstalled reports whether the recorder is armed in one window.
func postMessageInstalled(page *rod.Page) (bool, error) {
	res, err := page.Eval(postMessageInstalledJS)
	if err != nil {
		return false, err
	}
	return res.Value.Bool(), nil
}

// readPostMessageRing decodes one window's ring snapshot.
func readPostMessageRing(page *rod.Page) ([]PostMessage, int, error) {
	res, err := page.Eval(postMessageReadJS)
	if err != nil {
		return nil, 0, err
	}
	return parsePostMessageSnapshot(res.Value.Str())
}

// clearPostMessageRing empties one window's ring and returns the cleared count.
func clearPostMessageRing(page *rod.Page) (int, error) {
	res, err := page.Eval(postMessageClearJS)
	if err != nil {
		return 0, err
	}
	raw := res.Value.Str()
	if raw == "null" || raw == "" {
		return 0, nil
	}
	var cleared struct {
		Cleared int `json:"cleared"`
	}
	if err := json.Unmarshal([]byte(raw), &cleared); err != nil {
		return 0, err
	}
	return cleared.Cleared, nil
}

// parsePostMessageSnapshot decodes one read payload. The payload is the JSON
// string "null" when the recorder is not installed in that window.
func parsePostMessageSnapshot(raw string) ([]PostMessage, int, error) {
	if raw == "" || raw == "null" {
		return []PostMessage{}, 0, nil
	}
	var snap *postMessageSnapshot
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		return nil, 0, fmt.Errorf("decode postMessage log: %w", err)
	}
	if snap == nil {
		return []PostMessage{}, 0, nil
	}
	return snap.Messages, snap.Dropped, nil
}

// trimPostMessages orders the aggregate log, keeps the newest MaxPostMessages
// records, reports older buffered ones as truncated and re-clips every field so
// a tampered page cannot smuggle an oversized value into the response.
func trimPostMessages(
	messages []PostMessage,
	dropped int,
	frames []PostMessageFrame,
) PostMessageResult {
	out := PostMessageResult{
		Installed: true,
		Messages:  []PostMessage{},
		Dropped:   dropped,
		Frames:    frames,
	}
	sort.SliceStable(messages, func(i, j int) bool {
		return messages[i].TS < messages[j].TS
	})
	if len(messages) > MaxPostMessages {
		out.Truncated = len(messages) - MaxPostMessages
		messages = messages[len(messages)-MaxPostMessages:]
	}
	for _, m := range messages {
		m.Origin, _ = clipRunes(m.Origin, maxPostMessageFieldRunes)
		m.SourceKind, _ = clipRunes(m.SourceKind, maxPostMessageFieldRunes)
		m.Data, _ = clipRunes(m.Data, maxPostMessageDataRunes)
		if len(m.Stack) > maxPostMessageStackFrames {
			m.Stack = m.Stack[:maxPostMessageStackFrames]
		}
		for i := range m.Stack {
			m.Stack[i], _ = clipRunes(m.Stack[i], maxPostMessageFieldRunes)
		}
		out.Messages = append(out.Messages, m)
	}
	return out
}

// pageURL reads one window's live location.
func pageURL(page *rod.Page) string {
	res, err := page.Eval("() => location.href")
	if err != nil {
		return ""
	}
	return res.Value.Str()
}

// awaitPostMessageDelivery gives the queued message task one turn to run before
// the caller reads the ring, and aborts when the caller's deadline expires.
func awaitPostMessageDelivery(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(30 * time.Millisecond):
	}
}

// postMessageFrames enumerates the live iframes depth-first, up to
// PostMessageFrameCap. Cross-origin or not-yet-loaded frames whose document rod
// cannot promote are skipped, never fatal.
func postMessageFrames(ctx context.Context, main *rod.Page) []postMessageFrame {
	out := []postMessageFrame{}
	collectPostMessageFrames(ctx, main, &out)
	if len(out) > PostMessageFrameCap {
		return out[:PostMessageFrameCap]
	}
	return out
}

// collectPostMessageFrames appends each reachable iframe and recurses into it.
func collectPostMessageFrames(
	ctx context.Context,
	page *rod.Page,
	out *[]postMessageFrame,
) {
	if len(*out) >= PostMessageFrameCap {
		return
	}
	els, err := page.Context(ctx).Elements("iframe")
	if err != nil {
		return
	}
	for _, el := range els {
		if len(*out) >= PostMessageFrameCap {
			return
		}
		frame, err := el.Frame()
		if err != nil {
			continue
		}
		*out = append(*out, postMessageFrame{
			page: frame,
			url:  pageURL(frame.Context(ctx)),
		})
		collectPostMessageFrames(ctx, frame, out)
	}
}

// postMessageWindowSendJS builds the sender expression for a keyword window.
func postMessageWindowSendJS(window string) string {
	return `(payload, targetOrigin) => {
  const target = ` + window + `;
  target.postMessage(payload, targetOrigin);
  return true;
}`
}

// postMessageScriptTemplate is the recorder body: one capture-phase listener per
// window records window "message" events in a bounded ring. It never wraps
// addEventListener, so a listener the page registered or removed keeps its
// identity. The ring and field caps are filled from the Go constants so the
// page and the envelope cannot drift.
const postMessageScriptTemplate = `
if (window.__wmPostMessageInstalled) return;
const WM_PM_CAP = %d;
const WM_PM_DATA_MAX = %d;
const WM_PM_STACK_MAX = %d;
const WM_PM_FIELD_MAX = %d;
const WM_PM_RING = { messages: [], dropped: 0 };
window.__wmPostMessageRing = WM_PM_RING;
window.__wmPostMessageInstalled = true;

const wmPmClip = (raw) => {
  let s;
  try { s = String(raw); } catch (e) { s = ''; }
  if (s.length <= WM_PM_DATA_MAX) return s;
  let v = s.slice(0, WM_PM_DATA_MAX);
  const c = v.charCodeAt(v.length - 1);
  if (c >= 0xD800 && c <= 0xDBFF) v = v.slice(0, -1);
  return v;
};

const wmPmStack = () => {
  const out = [];
  try {
    const lines = String(new Error().stack || '').split('\n');
    for (let i = 0; i < lines.length && out.length < WM_PM_STACK_MAX; i++) {
      const line = lines[i].trim();
      if (!line || line.indexOf('Error') === 0) continue;
      const m = line.match(/^at\s+(?:(.+?)\s+\()?(.+):(\d+):(\d+)\)?$/);
      if (!m) continue;
      const fn = (m[1] || 'anonymous').replace(/^Object\./, '').replace(/^new /, '');
      const url = m[2] || '';
      if (fn.indexOf('wmPm') === 0 || url.indexOf('__wm') >= 0 || url.indexOf('web-mcp') >= 0) continue;
      const frame = fn + ' ' + (url || '<anonymous>') + ':' + m[3];
      out.push(frame.length > WM_PM_FIELD_MAX ? frame.slice(0, WM_PM_FIELD_MAX) : frame);
    }
  } catch (e) {}
  return out;
};

const wmPmKind = (src) => {
  try {
    if (!src) return 'unknown';
    if (src === window) return 'window';
    if (src.window === src) return 'frame';
    return 'worker';
  } catch (e) {
    return 'unknown';
  }
};

const wmPmData = (data) => {
  if (typeof data === 'string') return { data: wmPmClip(data), type: 'string' };
  if (data !== null && typeof data === 'object') {
    try {
      return { data: wmPmClip(JSON.stringify(data)), type: 'json' };
    } catch (e) {
      return { data: wmPmClip(data), type: 'object' };
    }
  }
  try {
    return { data: wmPmClip(data), type: 'other' };
  } catch (e) {
    return { data: '', type: 'other' };
  }
};

const wmPmRecord = (event) => {
  try {
    const payload = wmPmData(event.data);
    WM_PM_RING.messages.push({
      origin: wmPmClip(event.origin || ''),
      source_kind: wmPmKind(event.source),
      data: payload.data,
      data_type: payload.type,
      ts: Date.now(),
      stack: wmPmStack()
    });
    if (WM_PM_RING.messages.length > WM_PM_CAP) {
      WM_PM_RING.messages.shift();
      WM_PM_RING.dropped++;
    }
  } catch (e) {}
};

window.addEventListener('message', wmPmRecord, true);
`

// postMessageScript is the recorder body with the caps baked in.
var postMessageScript = fmt.Sprintf(
	postMessageScriptTemplate,
	PostMessageRingCap,
	maxPostMessageDataRunes,
	maxPostMessageStackFrames,
	maxPostMessageFieldRunes,
)

// postMessageInstallJS runs the recorder once in the current window: Page.Eval
// needs a function expression, so the shared body is wrapped and invoked.
var postMessageInstallJS = "() => {" + postMessageScript + "}"

// postMessageNewDocumentScript re-injects the recorder before each new
// document's own scripts. Init scripts run as raw statements, where the body's
// top-level guard return is a syntax error, so the body runs inside a
// self-invoking function.
var postMessageNewDocumentScript = "(() => {" + postMessageScript + "})()"

// postMessageInstalledJS reports whether the recorder is armed in this window.
const postMessageInstalledJS = `() => window.__wmPostMessageInstalled === true`

// postMessageReadJS returns one window's whole ring as a JSON string, or "null"
// when the recorder is not installed there.
const postMessageReadJS = `() => window.__wmPostMessageInstalled && window.__wmPostMessageRing
  ? JSON.stringify({ messages: window.__wmPostMessageRing.messages, dropped: window.__wmPostMessageRing.dropped || 0 })
  : 'null'`

// postMessageClearJS empties one window's ring and reports the cleared count, or
// "null" when the recorder is not installed there.
const postMessageClearJS = `() => {
  if (!window.__wmPostMessageInstalled || !window.__wmPostMessageRing) return 'null';
  const ring = window.__wmPostMessageRing;
  const cleared = ring.messages.length;
  ring.messages = [];
  ring.dropped = 0;
  return JSON.stringify({ cleared: cleared });
}`

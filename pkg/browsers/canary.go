package browsers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-rod/rod"
)

// CanaryFrameCap bounds how many iframes one inject_canary run touches, so a
// hostile page cannot turn the probe into unbounded work.
const CanaryFrameCap = 20

// CanaryTarget names one injection point that inject_canary can exercise.
type CanaryTarget string

const (
	// CanaryQuery injects the canary as the wmcanary query parameter and
	// reloads the page.
	CanaryQuery CanaryTarget = "query"
	// CanaryHash injects the canary into location.hash without a reload.
	CanaryHash CanaryTarget = "hash"
	// CanaryPostMessage posts a wm-probe message carrying the canary.
	CanaryPostMessage CanaryTarget = "postmessage"
	// CanaryName writes the canary to window.name.
	CanaryName CanaryTarget = "name"
)

// ErrCanaryNeedsTracer is returned when inject_canary runs before the
// source-to-sink tracer is armed. The MCP page_act surface prefixes it with
// "page_act: " to produce its documented error text.
var ErrCanaryNeedsTracer = errors.New(`inject_canary needs the sink tracer — run action "sinks_install" first`)

// CanaryInjection records which target was injected and where it landed: the
// reloaded page URL for query, or the frame URL for the other targets.
type CanaryInjection struct {
	// Target is the injection point: query, hash, postmessage or name.
	Target string `json:"target"`
	// Where is the page or frame URL that received the injection.
	Where string `json:"where"`
}

// CanaryHit is one sink that received the canary, projected from SinkHit to
// the canary report's shape.
type CanaryHit struct {
	// Sink is the instrumented API that received the canary.
	Sink string `json:"sink"`
	// Context labels the receiving channel: html, attr, url or js.
	Context string `json:"context"`
	// Selector is a short CSS path to the receiving node, when the sink has one.
	Selector string `json:"selector,omitempty"`
	// Value is the value the sink received, clipped by the tracer.
	Value string `json:"value"`
	// Stack is the page-side caller stack, instrumentation frames stripped.
	Stack []string `json:"stack,omitempty"`
}

// CanaryResult is the wire shape of inject_canary. Installed reports whether the
// tracer was armed and running in the current document.
type CanaryResult struct {
	// Canary is the value injected, generated when the caller omitted one.
	Canary string `json:"canary"`
	// Injected lists every injection point that actually ran.
	Injected []CanaryInjection `json:"injected"`
	// Hits are the sink calls whose value contained the canary.
	Hits []CanaryHit `json:"hits"`
	// Installed reports whether the tracer was armed for this page.
	Installed bool `json:"installed"`
}

// canaryFrame is one iframe promoted to its own rod page plus its live URL.
type canaryFrame struct {
	page *rod.Page
	url  string
}

// InjectCanary injects a unique canary into the requested client-side sources
// and returns the sinks that received it. Query appends wmcanary to the URL and
// reloads; hash, postmessage and name inject into the current document. Work is
// capped at CanaryFrameCap frames. It refuses to run unless InstallSinks armed
// the tracer for this page.
//
// The method takes the page lock itself: callers must NOT pre-lock, and the
// tracer guard runs before any browser work.
func (p *Page) InjectCanary(
	ctx context.Context,
	rawTargets []string,
	value string,
) (CanaryResult, error) {
	p.Lock()
	defer p.Unlock()
	if !p.sinkScriptInstalled {
		return CanaryResult{}, ErrCanaryNeedsTracer
	}
	if p.Rod == nil {
		return CanaryResult{}, fmt.Errorf("page session is closed")
	}

	targets, err := parseCanaryTargets(rawTargets)
	if err != nil {
		return CanaryResult{}, err
	}
	canary := strings.TrimSpace(value)
	if canary == "" {
		canary = newCanaryValue()
	}

	page := p.Rod.Context(ctx)
	out := CanaryResult{
		Canary:    canary,
		Injected:  []CanaryInjection{},
		Hits:      []CanaryHit{},
		Installed: true,
	}
	if _, err := page.Eval(sinkInstallJS); err != nil {
		return CanaryResult{}, fmt.Errorf("inject canary: arm tracer: %w", err)
	}

	if hasCanaryTarget(targets, CanaryQuery) {
		href, err := page.Eval("() => location.href")
		if err != nil {
			return CanaryResult{}, fmt.Errorf("inject canary: read page URL: %w", err)
		}
		target, err := withCanaryQuery(href.Value.Str(), canary)
		if err != nil {
			return CanaryResult{}, fmt.Errorf("inject canary: %w", err)
		}
		if err := page.Navigate(target); err != nil {
			return CanaryResult{}, fmt.Errorf("inject canary: navigate: %w", err)
		}
		if err := page.WaitLoad(); err != nil {
			return CanaryResult{}, fmt.Errorf("inject canary: wait for load: %w", err)
		}
		if _, err := page.Eval(sinkInstallJS); err != nil {
			return CanaryResult{}, fmt.Errorf("inject canary: re-arm tracer: %w", err)
		}
		out.Injected = append(out.Injected, CanaryInjection{
			Target: string(CanaryQuery),
			Where:  target,
		})
	}

	frames := canaryFrameWindow(append([]canaryFrame{{page: page}}, canaryFrames(page)...))
	for _, frame := range frames {
		if _, err := frame.page.Eval(sinkInstallJS); err != nil {
			continue
		}
		where := frame.url
		if where == "" {
			if href, err := frame.page.Eval("() => location.href"); err == nil {
				where = href.Value.Str()
			}
		}
		out.Injected = append(out.Injected, injectCanaryFrame(ctx, frame.page, targets, canary, where)...)
	}

	if hasCanaryTarget(targets, CanaryPostMessage) {
		awaitCanaryDelivery(ctx)
		if err := ctx.Err(); err != nil {
			return CanaryResult{}, fmt.Errorf("inject canary: %w", err)
		}
	}
	out.Hits = readCanaryHits(frames, canary)
	return out, nil
}

// injectCanaryFrame runs the non-query targets in one frame and reports the
// injection points that succeeded.
func injectCanaryFrame(
	ctx context.Context,
	frame *rod.Page,
	targets []CanaryTarget,
	canary, where string,
) []CanaryInjection {
	if err := ctx.Err(); err != nil {
		return nil
	}
	out := []CanaryInjection{}
	if hasCanaryTarget(targets, CanaryHash) {
		if _, err := frame.Eval(hashCanaryJS(canary)); err == nil {
			out = append(out, CanaryInjection{Target: string(CanaryHash), Where: where})
		}
	}
	if hasCanaryTarget(targets, CanaryPostMessage) {
		if _, err := frame.Eval(postMessageCanaryJS(canary)); err == nil {
			out = append(out, CanaryInjection{Target: string(CanaryPostMessage), Where: where})
		}
	}
	if hasCanaryTarget(targets, CanaryName) {
		if _, err := frame.Eval(nameCanaryJS(canary)); err == nil {
			out = append(out, CanaryInjection{Target: string(CanaryName), Where: where})
		}
	}
	return out
}

// canaryFrames enumerates the live iframes depth-first, up to CanaryFrameCap.
// Cross-origin or not-yet-loaded frames whose document rod cannot promote are
// skipped, never fatal.
func canaryFrames(main *rod.Page) []canaryFrame {
	out := []canaryFrame{}
	collectCanaryFrames(main, &out)
	return canaryFrameWindow(out)
}

// collectCanaryFrames appends each reachable iframe and recurses into it.
func collectCanaryFrames(page *rod.Page, out *[]canaryFrame) {
	if len(*out) >= CanaryFrameCap {
		return
	}
	els, err := page.Elements("iframe")
	if err != nil {
		return
	}
	for _, el := range els {
		if len(*out) >= CanaryFrameCap {
			return
		}
		frame, err := el.Frame()
		if err != nil {
			continue
		}
		href := ""
		if v, err := frame.Eval("() => location.href"); err == nil {
			href = v.Value.Str()
		}
		*out = append(*out, canaryFrame{page: frame, url: href})
		collectCanaryFrames(frame, out)
	}
}

// canaryFrameWindow enforces CanaryFrameCap on an already collected list.
func canaryFrameWindow(frames []canaryFrame) []canaryFrame {
	if len(frames) > CanaryFrameCap {
		return frames[:CanaryFrameCap]
	}
	return frames
}

// readCanaryHits reads every frame's tracer ring and keeps the hits whose value
// contains the canary, newest MaxSinkHits overall.
func readCanaryHits(frames []canaryFrame, canary string) []CanaryHit {
	hits := []CanaryHit{}
	for _, frame := range frames {
		res, err := frame.page.Eval(sinkReadJS)
		if err != nil {
			continue
		}
		snap, err := parseSinkSnapshot(res.Value.Str())
		if err != nil {
			continue
		}
		for _, hit := range snap.Hits {
			if !strings.Contains(hit.Value, canary) {
				continue
			}
			hits = append(hits, CanaryHit{
				Sink:     hit.Sink,
				Context:  hit.Context,
				Selector: hit.Selector,
				Value:    hit.Value,
				Stack:    hit.Stack,
			})
		}
	}
	if len(hits) > MaxSinkHits {
		hits = hits[len(hits)-MaxSinkHits:]
	}
	return hits
}

// parseCanaryTargets normalizes the target list: empty means all four targets,
// entries may be repeated or comma-separated, and unknown names are rejected.
func parseCanaryTargets(raw []string) ([]CanaryTarget, error) {
	if len(raw) == 0 {
		return defaultCanaryTargets(), nil
	}
	seen := map[CanaryTarget]bool{}
	out := []CanaryTarget{}
	for _, entry := range raw {
		for _, part := range strings.Split(entry, ",") {
			name := strings.ToLower(strings.TrimSpace(part))
			if name == "" {
				continue
			}
			target := CanaryTarget(name)
			if !validCanaryTarget(target) {
				return nil, fmt.Errorf("invalid canary target %q (must be query, hash, postmessage or name)", name)
			}
			if seen[target] {
				continue
			}
			seen[target] = true
			out = append(out, target)
		}
	}
	if len(out) == 0 {
		return defaultCanaryTargets(), nil
	}
	return out, nil
}

// defaultCanaryTargets returns the four injection points in ticket order.
func defaultCanaryTargets() []CanaryTarget {
	return []CanaryTarget{CanaryQuery, CanaryHash, CanaryPostMessage, CanaryName}
}

// validCanaryTarget reports whether target is one of the four injection points.
func validCanaryTarget(target CanaryTarget) bool {
	switch target {
	case CanaryQuery, CanaryHash, CanaryPostMessage, CanaryName:
		return true
	default:
		return false
	}
}

// hasCanaryTarget reports whether targets contains target.
func hasCanaryTarget(targets []CanaryTarget, target CanaryTarget) bool {
	for _, t := range targets {
		if t == target {
			return true
		}
	}
	return false
}

// newCanaryValue mints the default wm-canary-<8 hex> value.
func newCanaryValue() string {
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("wm-canary-%08x", uint32(time.Now().UnixNano()))
	}
	return "wm-canary-" + hex.EncodeToString(raw[:])
}

// withCanaryQuery sets the wmcanary query parameter on raw.
func withCanaryQuery(raw, canary string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse page URL %q: %w", raw, err)
	}
	q := u.Query()
	q.Set("wmcanary", canary)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// awaitCanaryDelivery gives queued postMessage tasks one turn to run before the
// rings are read, and aborts when the caller's deadline expires.
func awaitCanaryDelivery(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(30 * time.Millisecond):
	}
}

// hashCanaryJS sets the hash and dispatches hashchange without a reload.
func hashCanaryJS(canary string) string {
	return `() => {
  const canary = ` + jsLiteral(canary) + `;
  if (location.hash !== '#' + canary) location.hash = '#' + canary;
  try {
    window.dispatchEvent(new HashChangeEvent('hashchange'));
  } catch (e) {
    window.dispatchEvent(new Event('hashchange'));
  }
  return true;
}`
}

// postMessageCanaryJS posts the wm-probe object to this window.
func postMessageCanaryJS(canary string) string {
	return `() => {
  window.postMessage({ type: 'wm-probe', data: ` + jsLiteral(canary) + ` }, '*');
  return true;
}`
}

// nameCanaryJS writes the canary to window.name.
func nameCanaryJS(canary string) string {
	return `() => {
  window.name = ` + jsLiteral(canary) + `;
  return true;
}`
}

// jsLiteral renders s as a JSON string, which is also a valid JS string literal.
func jsLiteral(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

package browsers

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

const (
	// InterceptBlock fails a matching request with a client-blocked network error.
	InterceptBlock = "block"
	// InterceptAllow lets a matching request continue unchanged.
	InterceptAllow = "allow"
	// InterceptRewrite continues a matching request with overridden headers or body.
	InterceptRewrite = "rewrite"
	// InterceptFulfill answers a matching request with a synthetic response.
	InterceptFulfill = "fulfill"
	// InterceptScope marks a request blocked because its host is outside the
	// browser's scope allowlist rather than by a user rule.
	InterceptScope = "scope"

	// interceptRuleCap bounds how many rules one page may hold.
	interceptRuleCap = 50
	// interceptHeaderCap bounds the header overrides one rule may carry.
	interceptHeaderCap = 32
	// interceptDelayCap bounds a rule's delay so a rule cannot stall a page.
	interceptDelayCap = 10 * time.Second
	// interceptResolveTimeout bounds one paused request's CDP resolution.
	interceptResolveTimeout = 20 * time.Second
	// interceptStopTimeout bounds continuing paused requests and disabling Fetch.
	interceptStopTimeout = 5 * time.Second
	// interceptConcurrency bounds the resolution goroutines one page may run.
	interceptConcurrency = 64
)

// InterceptRule is one page-scoped in-flight request rule. URLPattern is a
// wildcard glob matched against the full request URL ("*" matches anything),
// Method optionally narrows the match, and Action is InterceptBlock,
// InterceptAllow, InterceptRewrite or InterceptFulfill. Rewrite applies Headers,
// a non-empty Body and RewriteMethod to the outgoing request; fulfill answers
// with Status (default 200), Headers and Body instead of hitting the network.
// DelayMS is clamped to interceptDelayCap.
type InterceptRule struct {
	// ID names the rule and its network-ring hits; generated when empty.
	ID string `json:"id"`
	// URLPattern is the wildcard glob matched against the full request URL.
	URLPattern string `json:"url_pattern"`
	// Method narrows the rule to one HTTP method; empty matches every method.
	Method string `json:"method,omitempty"`
	// Action is block, allow, rewrite or fulfill.
	Action string `json:"action"`
	// Headers overrides request headers for rewrite and response headers for fulfill.
	Headers map[string]string `json:"headers,omitempty"`
	// RewriteMethod overrides the outgoing method for rewrite; empty keeps it.
	RewriteMethod string `json:"rewrite_method,omitempty"`
	// Status is the synthetic response status used by fulfill.
	Status int `json:"status,omitempty"`
	// Body overrides the request payload for rewrite and is the response payload for fulfill.
	Body string `json:"body,omitempty"`
	// DelayMS delays the resolution, bounded by interceptDelayCap.
	DelayMS int `json:"delay_ms,omitempty"`
}

// InterceptSnapshot is a page's rule set with its remaining rule budget.
type InterceptSnapshot struct {
	// Rules holds the page's rules with sensitive values redacted.
	Rules []InterceptRule
	// Remaining is how many more rules the page can hold.
	Remaining int
}

// InterceptRuleLimitError reports that a batch would exceed a page's rule
// budget. A page holds its rules across calls, so the cap is page-wide.
type InterceptRuleLimitError struct {
	// Held is the number of rules the page already holds.
	Held int
	// Added is the number of new rules the batch would add.
	Added int
	// Cap is the page-wide rule budget.
	Cap int
}

// Error renders the refusal with the cap and the recovery.
func (e *InterceptRuleLimitError) Error() string {
	return fmt.Sprintf(
		"intercept: page already holds %d rules; adding %d more exceeds the %d-rule limit (clear rules first)",
		e.Held,
		e.Added,
		e.Cap,
	)
}

// compiledIntercept is one validated rule with its pattern compiled.
type compiledIntercept struct {
	rule    InterceptRule
	pattern *regexp.Regexp
}

// cdpCommand is one CDP command the interceptor can send.
type cdpCommand interface {
	Call(proto.Client) error
}

// cdpSender sends one CDP command for the interceptor's page within ctx.
type cdpSender interface {
	Send(ctx context.Context, msg cdpCommand) error
}

// rodSender sends CDP commands to a live rod page.
type rodSender struct {
	page *rod.Page
}

// Send runs one command on the sender's page under ctx.
func (s rodSender) Send(ctx context.Context, msg cdpCommand) error {
	return msg.Call(s.page.Context(ctx))
}

// interceptor is a page's live CDP Fetch hook. It owns its own mutex and never
// touches Page.mu while a CDP call is in flight. A page holds at most one.
type interceptor struct {
	mu      sync.Mutex
	rules   []compiledIntercept
	paused  map[proto.FetchRequestID]struct{}
	pending []proto.FetchRequestID
	stopped bool
	nextID  int
	page    *rod.Page
	obs     *pageObserver
	browser *Browser
	sem     chan struct{}
	done    chan struct{}
	cancel  context.CancelFunc
	stopDo  sync.Once
	cdp     cdpSender
	cacheMu sync.Mutex
	// cacheDisabled reports whether this interceptor turned the page cache off
	// and therefore owes a restore.
	cacheDisabled bool
}

// newInterceptor returns an unarmed interceptor for page, enforcing browser's
// request scope on every paused request.
func newInterceptor(page *rod.Page, obs *pageObserver, browser *Browser) *interceptor {
	return &interceptor{
		paused:  make(map[proto.FetchRequestID]struct{}),
		page:    page,
		obs:     obs,
		browser: browser,
		sem:     make(chan struct{}, interceptConcurrency),
		done:    make(chan struct{}),
		cdp:     rodSender{page: page},
	}
}

// start subscribes to pauses before enabling the CDP Fetch domain, so no
// request is paused between enable and the first delivered event.
func (i *interceptor) start(ctx context.Context) error {
	capture, cancel := i.page.WithCancel()
	i.page = capture
	i.cdp = rodSender{page: capture}
	i.cancel = cancel
	stop := capture.EachEvent(func(e *proto.FetchRequestPaused) { i.dispatch(e) })
	go stop()
	enable := proto.FetchEnable{
		Patterns: []*proto.FetchRequestPattern{{URLPattern: "*"}},
	}
	callCtx, stopCall := context.WithTimeout(ctx, interceptResolveTimeout)
	defer stopCall()
	if err := enable.Call(capture.Context(callCtx)); err != nil {
		cancel()
		return fmt.Errorf("intercept: enable fetch domain: %w", err)
	}
	return nil
}

// dispatch records the pause and resolves it on a bounded goroutine. A page
// already at interceptConcurrency continues the request inline instead of
// queueing it.
func (i *interceptor) dispatch(e *proto.FetchRequestPaused) {
	if e.Request == nil {
		return
	}
	if e.ResponseStatusCode != nil || e.ResponseErrorReason != "" {
		_ = i.call(proto.FetchContinueRequest{RequestID: e.RequestID})
		return
	}
	i.mu.Lock()
	if i.stopped {
		i.mu.Unlock()
		_ = i.call(proto.FetchContinueRequest{RequestID: e.RequestID})
		return
	}
	i.paused[e.RequestID] = struct{}{}
	i.mu.Unlock()

	select {
	case i.sem <- struct{}{}:
		go func() {
			defer func() {
				<-i.sem
				i.forget(e.RequestID)
			}()
			i.resolveSafely(e)
		}()
	default:
		_ = i.call(proto.FetchContinueRequest{RequestID: e.RequestID})
		i.forget(e.RequestID)
	}
}

// forget drops a resolved request from the paused set.
func (i *interceptor) forget(id proto.FetchRequestID) {
	i.mu.Lock()
	delete(i.paused, id)
	i.mu.Unlock()
}

// call runs one CDP command under the interceptor's resolve timeout.
func (i *interceptor) call(msg cdpCommand) error {
	ctx, cancel := context.WithTimeout(context.Background(), interceptResolveTimeout)
	defer cancel()
	return i.cdp.Send(ctx, msg)
}

// resolveSafely keeps a handler panic from escaping the event loop: a panic
// continues the paused request instead of leaving the page stalled.
func (i *interceptor) resolveSafely(e *proto.FetchRequestPaused) {
	defer func() {
		if recover() != nil {
			_ = i.call(proto.FetchContinueRequest{RequestID: e.RequestID})
		}
	}()
	i.resolve(e)
}

// resolve applies the browser's request scope, then the first matching rule, or
// continues the request when neither refuses it. A resolution failure on a
// document request disables interception, so a broken rule can never leave a
// navigation paused.
func (i *interceptor) resolve(e *proto.FetchRequestPaused) {
	if i.scopeDenied(e.Request.URL) {
		i.blockOutOfScope(e)
		return
	}
	rule, hit := i.match(e.Request.Method, e.Request.URL)
	if !hit {
		_ = i.call(proto.FetchContinueRequest{RequestID: e.RequestID})
		return
	}
	if rule.DelayMS > 0 {
		timer := time.NewTimer(time.Duration(rule.DelayMS) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-i.done:
			return
		}
	}
	if err := i.apply(e, rule); err != nil {
		if e.ResourceType == proto.NetworkResourceTypeDocument {
			go i.stop(context.Background())
		}
		return
	}
	if i.obs != nil {
		i.obs.recordIntercept(e, rule)
		if rule.Action == InterceptRewrite {
			i.obs.recordSentRequest(e, rule)
		}
	}
}

// scopeDenied reports whether a paused request is outside the browser's request
// allowlist. A page with no browser or an open allowlist denies nothing.
func (i *interceptor) scopeDenied(rawURL string) bool {
	if i.browser == nil || rawURL == "" {
		return false
	}
	return i.browser.CheckRequestScope(rawURL) != nil
}

// blockOutOfScope fails a paused request whose host is outside the browser's
// allowlist and records it as intercepted by "scope" rather than silently
// dropping it.
func (i *interceptor) blockOutOfScope(e *proto.FetchRequestPaused) {
	_ = i.call(proto.FetchFailRequest{
		RequestID:   e.RequestID,
		ErrorReason: proto.NetworkErrorReasonBlockedByClient,
	})
	if i.obs != nil {
		i.obs.recordIntercept(e, InterceptRule{ID: InterceptScope, Action: InterceptScope})
	}
}

// apply answers one paused request with the rule's action.
func (i *interceptor) apply(e *proto.FetchRequestPaused, rule InterceptRule) error {
	switch rule.Action {
	case InterceptBlock:
		return i.call(proto.FetchFailRequest{
			RequestID:   e.RequestID,
			ErrorReason: proto.NetworkErrorReasonBlockedByClient,
		})
	case InterceptRewrite:
		return i.call(proto.FetchContinueRequest{
			RequestID: e.RequestID,
			Method:    rule.RewriteMethod,
			Headers:   mergeInterceptHeaders(e.Request.Headers, rule.Headers),
			PostData:  interceptPostData(rule.Body),
		})
	case InterceptFulfill:
		return i.call(proto.FetchFulfillRequest{
			RequestID:       e.RequestID,
			ResponseCode:    rule.Status,
			ResponseHeaders: fulfillHeaders(rule.Headers, rule.Body != ""),
			Body:            []byte(rule.Body),
		})
	default:
		return i.call(proto.FetchContinueRequest{RequestID: e.RequestID})
	}
}

// match returns the first rule matching the method and URL, with its header map
// copied so the caller holds no internal state.
func (i *interceptor) match(method, rawURL string) (InterceptRule, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, r := range i.rules {
		if r.rule.Method != "" && !strings.EqualFold(r.rule.Method, method) {
			continue
		}
		if r.pattern.MatchString(rawURL) {
			return cloneInterceptRule(r.rule), true
		}
	}
	return InterceptRule{}, false
}

// upsert adds or replaces rules by id and returns the page's full rule list with
// its remaining budget and sensitive header values redacted.
func (i *interceptor) upsert(rules []compiledIntercept) (InterceptSnapshot, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	index := make(map[string]int, len(i.rules))
	for idx, r := range i.rules {
		index[r.rule.ID] = idx
	}
	added := 0
	for _, r := range rules {
		if _, ok := index[r.rule.ID]; !ok {
			added++
		}
	}
	if len(i.rules)+added > interceptRuleCap {
		return InterceptSnapshot{}, &InterceptRuleLimitError{
			Held:  len(i.rules),
			Added: added,
			Cap:   interceptRuleCap,
		}
	}
	for _, r := range rules {
		if idx, ok := index[r.rule.ID]; ok {
			i.rules[idx] = r
			continue
		}
		i.rules = append(i.rules, r)
	}
	return InterceptSnapshot{
		Rules:     i.listLocked(),
		Remaining: interceptRuleCap - len(i.rules),
	}, nil
}

// addRules stores a validated batch and turns the page cache off so the new
// rules see cached subresources. It returns the page's snapshot.
func (i *interceptor) addRules(ctx context.Context, rules []compiledIntercept) (InterceptSnapshot, error) {
	snapshot, err := i.upsert(rules)
	if err != nil {
		return InterceptSnapshot{}, err
	}
	if err := i.disableCache(ctx); err != nil {
		return InterceptSnapshot{}, err
	}
	return snapshot, nil
}

// list returns a copy of the page's rules with sensitive header values redacted.
func (i *interceptor) list() []InterceptRule {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.listLocked()
}

// listLocked renders the rules with secrets redacted; the caller holds i.mu.
func (i *interceptor) listLocked() []InterceptRule {
	out := make([]InterceptRule, len(i.rules))
	for idx, r := range i.rules {
		out[idx] = redactedInterceptRule(r.rule)
	}
	return out
}

// ruleCount reports how many rules are stored.
func (i *interceptor) ruleCount() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return len(i.rules)
}

// isStopped reports whether the interceptor has been disarmed.
func (i *interceptor) isStopped() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.stopped
}

// disarm stops consuming pauses, drops the rules and remembers the requests
// still paused so stop can continue them. It makes no CDP call, so a page
// teardown path may call it under the page lock.
func (i *interceptor) disarm() {
	i.stopDo.Do(func() {
		i.mu.Lock()
		i.stopped = true
		for id := range i.paused {
			i.pending = append(i.pending, id)
		}
		i.rules = nil
		i.mu.Unlock()
		close(i.done)
		if i.cancel != nil {
			i.cancel()
		}
	})
}

// stop disarms, continues every request still paused, disables the Fetch domain,
// restores the page cache and drops the rules. It is idempotent and safe while
// requests are resolving.
func (i *interceptor) stop(ctx context.Context) {
	i.disarm()
	i.mu.Lock()
	ids := i.pending
	i.pending = nil
	i.mu.Unlock()

	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), interceptStopTimeout)
	defer cancel()
	for _, id := range ids {
		_ = i.cdp.Send(callCtx, proto.FetchContinueRequest{RequestID: id})
	}
	_ = i.cdp.Send(callCtx, proto.FetchDisable{})
	i.restoreCache(callCtx)
}

// disableCache turns the page cache off so a cached subresource still reaches
// the Fetch hook. A second call is a no-op, and a stopped interceptor never
// arms the flag because its stop path has already restored it.
func (i *interceptor) disableCache(ctx context.Context) error {
	i.cacheMu.Lock()
	defer i.cacheMu.Unlock()
	if i.cacheDisabled || i.isStopped() {
		return nil
	}
	if err := i.cdp.Send(ctx, proto.NetworkSetCacheDisabled{CacheDisabled: true}); err != nil {
		return fmt.Errorf("intercept: disable page cache: %w", err)
	}
	i.cacheDisabled = true
	return nil
}

// restoreCache re-enables the page cache when this interceptor disabled it. A
// page with no armed cache flag makes no CDP call.
func (i *interceptor) restoreCache(ctx context.Context) {
	i.cacheMu.Lock()
	defer i.cacheMu.Unlock()
	if !i.cacheDisabled {
		return
	}
	i.cacheDisabled = false
	_ = i.cdp.Send(ctx, proto.NetworkSetCacheDisabled{CacheDisabled: false})
}

// InterceptAdd validates and adds or replaces rules on the page, arming the
// interception hook on first use. Rules are per page, bounded to
// interceptRuleCap across calls, and disappear when the page closes. While any
// rule is armed the page cache is off, so a cached subresource still matches.
// The returned snapshot is the page's full rule set with sensitive header values
// redacted, plus the remaining rule budget.
func (p *Page) InterceptAdd(ctx context.Context, rules []InterceptRule) (InterceptSnapshot, error) {
	if len(rules) == 0 {
		return InterceptSnapshot{}, fmt.Errorf("intercept: no rules supplied (pass at least one rule)")
	}
	p.interceptMu.Lock()
	used := make(map[string]bool)
	next := 1
	if it := p.intercept; it != nil && !it.isStopped() {
		for _, r := range it.list() {
			used[r.ID] = true
		}
		next = it.nextID + 1
	}
	normalized, next, err := assignInterceptIDs(rules, used, next)
	if err != nil {
		p.interceptMu.Unlock()
		return InterceptSnapshot{}, err
	}
	compiled, err := compileInterceptBatch(normalized)
	p.interceptMu.Unlock()
	if err != nil {
		return InterceptSnapshot{}, err
	}

	it, err := p.interceptor(ctx)
	if err != nil {
		return InterceptSnapshot{}, err
	}
	p.interceptMu.Lock()
	defer p.interceptMu.Unlock()
	if it.isStopped() {
		return InterceptSnapshot{}, fmt.Errorf("intercept: page interception was disarmed while adding rules — retry")
	}
	it.nextID = max(it.nextID, next-1)
	return it.addRules(ctx, compiled)
}

// interceptor returns the page's armed interceptor, arming a fresh one when the
// page has none. The page handle is read under Page.mu first, then interceptMu
// serializes the enable so two adders cannot arm competing hooks.
func (p *Page) interceptor(ctx context.Context) (*interceptor, error) {
	p.interceptMu.Lock()
	it := p.intercept
	if it != nil && it.isStopped() {
		it = nil
		p.intercept = nil
	}
	p.interceptMu.Unlock()
	if it != nil {
		return it, nil
	}

	if p.httpAuthArmed() {
		return nil, fmt.Errorf(
			"intercept: page %s has HTTP basic-auth credentials armed, which owns the CDP Fetch domain — clear the credentials first",
			p.ID,
		)
	}

	rodPage, err := p.rodPageForIntercept(ctx)
	if err != nil {
		return nil, err
	}

	p.interceptMu.Lock()
	defer p.interceptMu.Unlock()
	if it := p.intercept; it != nil && !it.isStopped() {
		return it, nil
	}
	fresh := newInterceptor(rodPage, p.observer, p.browser)
	if err := fresh.start(ctx); err != nil {
		return nil, err
	}
	p.intercept = fresh
	return fresh, nil
}

// InterceptList returns the page's rules with sensitive header values redacted,
// the remaining rule budget, and whether interception is currently armed.
func (p *Page) InterceptList() (InterceptSnapshot, bool) {
	p.interceptMu.Lock()
	it := p.intercept
	p.interceptMu.Unlock()
	if it == nil || it.isStopped() {
		return InterceptSnapshot{Rules: []InterceptRule{}, Remaining: interceptRuleCap}, false
	}
	rules := it.list()
	return InterceptSnapshot{
		Rules:     rules,
		Remaining: interceptRuleCap - len(rules),
	}, true
}

// InterceptClear removes every rule, continues any paused request and disables
// the Fetch domain. It returns how many rules were removed.
func (p *Page) InterceptClear(ctx context.Context) int {
	p.interceptMu.Lock()
	it := p.intercept
	p.intercept = nil
	p.interceptMu.Unlock()
	if it == nil {
		return 0
	}
	n := it.ruleCount()
	it.stop(ctx)
	return n
}

// rodPageForIntercept reads the live rod page under a bounded page lock, never
// keeping Page.mu across the subsequent Fetch enable.
func (p *Page) rodPageForIntercept(ctx context.Context) (*rod.Page, error) {
	if err := p.LockContext(ctx, "intercept"); err != nil {
		return nil, err
	}
	defer p.Unlock()
	if p.Rod == nil {
		return nil, fmt.Errorf("intercept: page session is closed — reopen the page and retry")
	}
	return p.Rod, nil
}

// stopInterception disarms the page's interceptor outside Page.mu so a CDP call
// is never made while the operation lock is held.
func (p *Page) stopInterception(ctx context.Context) {
	p.interceptMu.Lock()
	it := p.intercept
	p.intercept = nil
	p.interceptMu.Unlock()
	if it != nil {
		it.stop(ctx)
	}
}

// disarmInterception stops the page's interceptor without a CDP call, for a
// teardown path that already holds the page lock and is about to close the tab.
func (p *Page) disarmInterception() {
	p.interceptMu.Lock()
	it := p.intercept
	p.intercept = nil
	p.interceptMu.Unlock()
	if it != nil {
		it.disarm()
	}
}

// assignInterceptIDs copies rules, trimming fields and minting a unique id for
// every rule that has none. It rejects duplicate ids inside the batch.
func assignInterceptIDs(
	rules []InterceptRule,
	used map[string]bool,
	next int,
) ([]InterceptRule, int, error) {
	out := make([]InterceptRule, len(rules))
	for idx, r := range rules {
		r.ID = strings.TrimSpace(r.ID)
		if r.ID == "" {
			for {
				r.ID = fmt.Sprintf("r%d", next)
				next++
				if !used[r.ID] {
					break
				}
			}
		}
		if used[r.ID] {
			return nil, next, fmt.Errorf("intercept: duplicate rule id %q in the batch", r.ID)
		}
		used[r.ID] = true
		out[idx] = r
	}
	return out, next, nil
}

// compileInterceptBatch validates every rule and compiles its URL glob.
func compileInterceptBatch(rules []InterceptRule) ([]compiledIntercept, error) {
	out := make([]compiledIntercept, 0, len(rules))
	for _, r := range rules {
		c, err := compileInterceptRule(r)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// compileInterceptRule normalizes one rule and compiles its pattern.
func compileInterceptRule(r InterceptRule) (compiledIntercept, error) {
	r.ID = strings.TrimSpace(r.ID)
	if r.ID == "" {
		return compiledIntercept{}, fmt.Errorf("intercept: rule id is required")
	}
	r.URLPattern = strings.TrimSpace(r.URLPattern)
	if r.URLPattern == "" {
		r.URLPattern = "*"
	}
	r.Method = strings.ToUpper(strings.TrimSpace(r.Method))
	r.Action = strings.ToLower(strings.TrimSpace(r.Action))
	switch r.Action {
	case InterceptBlock, InterceptAllow, InterceptRewrite, InterceptFulfill:
	default:
		return compiledIntercept{}, fmt.Errorf(
			"intercept: rule %q has action %q (must be block, allow, rewrite or fulfill)",
			r.ID,
			r.Action,
		)
	}
	r.RewriteMethod = strings.ToUpper(strings.TrimSpace(r.RewriteMethod))
	if r.RewriteMethod != "" {
		if r.Action != InterceptRewrite {
			return compiledIntercept{}, fmt.Errorf(
				"intercept: rule %q sets rewrite_method but action is %q (rewrite_method needs action=rewrite)",
				r.ID,
				r.Action,
			)
		}
		if strings.ContainsAny(r.RewriteMethod, " \t\r\n") {
			return compiledIntercept{}, fmt.Errorf(
				"intercept: rule %q has rewrite_method %q (must be one HTTP method token)",
				r.ID,
				r.RewriteMethod,
			)
		}
	}
	if r.Action == InterceptFulfill && r.Status == 0 {
		r.Status = 200
	}
	if r.Status != 0 && (r.Status < 100 || r.Status > 599) {
		return compiledIntercept{}, fmt.Errorf(
			"intercept: rule %q has status %d (must be 100-599)",
			r.ID,
			r.Status,
		)
	}
	if r.DelayMS < 0 {
		return compiledIntercept{}, fmt.Errorf("intercept: rule %q has negative delay_ms", r.ID)
	}
	if cap := int(interceptDelayCap / time.Millisecond); r.DelayMS > cap {
		r.DelayMS = cap
	}
	if len(r.Headers) > interceptHeaderCap {
		return compiledIntercept{}, fmt.Errorf(
			"intercept: rule %q carries %d headers (max %d)",
			r.ID,
			len(r.Headers),
			interceptHeaderCap,
		)
	}
	headers := make(map[string]string, len(r.Headers))
	for name, value := range r.Headers {
		name = strings.TrimSpace(name)
		if name == "" {
			return compiledIntercept{}, fmt.Errorf("intercept: rule %q has an empty header name", r.ID)
		}
		headers[name] = strings.TrimSpace(value)
	}
	if len(headers) > 0 {
		r.Headers = headers
	} else {
		r.Headers = nil
	}
	pattern, err := compileInterceptPattern(r.URLPattern)
	if err != nil {
		return compiledIntercept{}, fmt.Errorf("intercept: rule %q pattern %q: %w", r.ID, r.URLPattern, err)
	}
	return compiledIntercept{rule: r, pattern: pattern}, nil
}

// compileInterceptPattern turns a CDP-style wildcard glob into an anchored
// regexp: "*" matches any run, "?" matches exactly one rune, everything else is
// literal.
func compileInterceptPattern(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("(?s)^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// cloneInterceptRule copies a rule and its header map for engine use; secrets
// stay intact because the action needs their real values.
func cloneInterceptRule(r InterceptRule) InterceptRule {
	if r.Headers == nil {
		return r
	}
	headers := make(map[string]string, len(r.Headers))
	for name, value := range r.Headers {
		headers[name] = value
	}
	r.Headers = headers
	return r
}

// redactedInterceptRule copies a rule for a result, replacing sensitive header
// and body values with the capture marker so a listing never leaks a secret.
func redactedInterceptRule(r InterceptRule) InterceptRule {
	r = cloneInterceptRule(r)
	r = redactInterceptHeaders(r)
	if r.Body != "" {
		r.Body, _ = redactBody(r.Body, false)
	}
	return r
}

// redactInterceptHeaders replaces sensitive header values with the capture
// marker so a rule listing never leaks a secret, and bounds each value.
func redactInterceptHeaders(r InterceptRule) InterceptRule {
	if len(r.Headers) == 0 {
		return r
	}
	for name, value := range r.Headers {
		if sensitiveHeaders[strings.ToLower(name)] {
			value = redactedValue
		}
		r.Headers[name] = cutString(value, networkHeaderValueLimit)
	}
	return r
}

// mergeInterceptHeaders merges the rule's header overrides onto the paused
// request's headers, which CDP replaces wholesale. It returns nil when there is
// nothing to override.
func mergeInterceptHeaders(
	base proto.NetworkHeaders,
	override map[string]string,
) []*proto.FetchHeaderEntry {
	if len(override) == 0 {
		return nil
	}
	merged := make(map[string]*proto.FetchHeaderEntry, len(base)+len(override))
	for name, jsonValue := range base {
		merged[strings.ToLower(name)] = &proto.FetchHeaderEntry{Name: name, Value: jsonValue.Str()}
	}
	for name, value := range override {
		merged[strings.ToLower(name)] = &proto.FetchHeaderEntry{Name: name, Value: value}
	}
	out := make([]*proto.FetchHeaderEntry, 0, len(merged))
	for _, entry := range merged {
		out = append(out, entry)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// sentNetworkHeaders renders the headers actually sent after a rewrite as the
// capture type, so the network ring can store and redact them. It returns nil
// when the rule overrides no headers.
func sentNetworkHeaders(request *proto.NetworkRequest, rule InterceptRule) proto.NetworkHeaders {
	entries := mergeInterceptHeaders(request.Headers, rule.Headers)
	if len(entries) == 0 {
		return nil
	}
	out := make(proto.NetworkHeaders, len(entries))
	for _, header := range entries {
		out[header.Name] = gson.New(header.Value)
	}
	return out
}

// recordSentRequest rewrites the network ring entry with the method and headers
// a rewrite rule actually sent, so the ring is the truth. It writes nothing when
// the request has no ring entry yet.
func (o *pageObserver) recordSentRequest(e *proto.FetchRequestPaused, rule InterceptRule) {
	if e.Request == nil || e.NetworkID == "" {
		return
	}
	headers, cut := redactHeaders(sentNetworkHeaders(e.Request, rule), o.secrets)
	o.mu.Lock()
	defer o.mu.Unlock()
	idx := o.indexOfRequest(e.NetworkID)
	if idx < 0 {
		return
	}
	entry := &o.net.items[idx]
	if rule.RewriteMethod != "" {
		entry.Method = rule.RewriteMethod
	}
	if headers != nil {
		entry.ReqHeaders = headers
		entry.ReqHeadersTruncated = cut
	}
}

// fulfillHeaders renders the synthetic response headers, adding a text/html// content type when a body is present and the rule supplied none.
func fulfillHeaders(override map[string]string, hasBody bool) []*proto.FetchHeaderEntry {
	headers := make(map[string]*proto.FetchHeaderEntry, len(override)+1)
	for name, value := range override {
		headers[strings.ToLower(name)] = &proto.FetchHeaderEntry{Name: name, Value: value}
	}
	if hasBody {
		if _, ok := headers["content-type"]; !ok {
			headers["content-type"] = &proto.FetchHeaderEntry{
				Name:  "Content-Type",
				Value: "text/html; charset=utf-8",
			}
		}
	}
	out := make([]*proto.FetchHeaderEntry, 0, len(headers))
	for _, entry := range headers {
		out = append(out, entry)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// interceptPostData returns the rewrite payload, or nil when the rule supplies
// none (CDP keeps the original bytes for a nil slice).
func interceptPostData(body string) []byte {
	if body == "" {
		return nil
	}
	return []byte(body)
}

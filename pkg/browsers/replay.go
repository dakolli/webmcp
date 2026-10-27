package browsers

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-rod/rod/lib/proto"
)

const (
	// replayTimeout caps one replay, redirects included.
	replayTimeout = 30 * time.Second
	// replayMaxRedirects bounds how many redirect hops a replay follows.
	replayMaxRedirects = 5
	// replayBodyCap is the ceiling on a returned response body: 256 KB.
	replayBodyCap = 256 << 10
)

// ErrReplayTargetAmbiguous reports a target that matched more than one captured
// entry. The message lists every candidate.
var ErrReplayTargetAmbiguous = errors.New("replay target is ambiguous")

// ErrReplayTargetNotFound reports a target that matched no captured entry.
var ErrReplayTargetNotFound = errors.New("replay target was not found")

// replayDroppedHeaders lists, lowercased, the captured request headers that are
// not copied onto a replay. The cookie jar owns Cookie, the Go transport owns
// Host, framing and connection headers, and Accept-Encoding is left unset so the
// transport negotiates gzip and transparently decodes it.
var replayDroppedHeaders = map[string]bool{
	"accept-encoding":     true,
	"connection":          true,
	"content-length":      true,
	"cookie":              true,
	"host":                true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"proxy-connection":    true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
}

// ReplaySpec selects one captured request and the overrides applied to it. A
// zero Method keeps the captured method, nil Headers keeps the captured headers,
// and Body replaces the captured body only when BodySet is true.
type ReplaySpec struct {
	// Target is a network sequence number or a unique substring of the captured URL.
	Target string
	// Method overrides the captured request method when non-empty.
	Method string
	// Headers overrides captured request headers; names are case-insensitive.
	Headers map[string]string
	// Body is the request body sent when BodySet is true.
	Body string
	// BodySet makes Body authoritative, including an explicitly empty body.
	BodySet bool
	// CheckScope authorizes a URL before a replay requests it. The surface
	// wires the manager's scope check here; nil skips the check.
	CheckScope func(rawURL string) error
	// MaxBytes caps the returned body. Zero or a value above replayBodyCap uses
	// the 256 KB ceiling; a larger body reports Truncated.
	MaxBytes int64
}

// ReplayHop is one request/response step of a replay redirect chain.
type ReplayHop struct {
	// Status is the HTTP status of this hop.
	Status int `json:"status"`
	// URL is the URL requested at this hop.
	URL string `json:"url"`
}

// ReplaySent describes the request a replay actually sent, with sensitive
// header values redacted unless the page opted into secrets capture.
type ReplaySent struct {
	// Method is the method used for the first hop.
	Method string `json:"method"`
	// URL is the captured URL requested by the first hop.
	URL string `json:"url"`
	// Headers are the sent headers, redacted per the capture rule.
	Headers map[string]string `json:"headers,omitempty"`
}

// ReplayResult is the outcome of a replay: the final response, the request that
// produced it, and the redirect chain when more than one hop ran.
type ReplayResult struct {
	// Status is the final HTTP status.
	Status int `json:"status"`
	// Headers are the final response headers, redacted per the capture rule.
	Headers map[string][]string `json:"headers,omitempty"`
	// Body carries UTF-8 response content with secrets redacted.
	Body string `json:"body,omitempty"`
	// Base64 carries binary response content, exclusive with Body.
	Base64 string `json:"base64,omitempty"`
	// Bytes is the size of the returned payload after the cap.
	Bytes int64 `json:"bytes"`
	// Truncated reports a payload cut at the requested cap.
	Truncated bool `json:"truncated"`
	// BodyRedactions counts the body values replaced with <redacted>.
	BodyRedactions int `json:"body_redactions"`
	// BodyUncaptured reports that the captured entry retained no request body
	// and the caller supplied none, so the replay sent no payload.
	BodyUncaptured bool `json:"body_uncaptured,omitempty"`
	// MS is the whole replay duration, redirects included.
	MS int64 `json:"ms"`
	// Request is the first-hop request.
	Request ReplaySent `json:"request"`
	// Chain lists every hop when the replay followed a redirect; it is [] for a
	// single-hop replay.
	Chain []ReplayHop `json:"chain"`
}

// ReplayTransportError reports a replay request the HTTP client could not
// complete. Err carries the transport cause so callers can tell a timeout from
// a failed request.
type ReplayTransportError struct {
	// Method is the request method at the failing hop.
	Method string
	// URL is the requested URL at the failing hop.
	URL string
	// Budget is the whole-replay timeout that applied.
	Budget time.Duration
	// Err is the transport cause.
	Err error
}

// Error names the method, target and, for a timeout, the ms budget. It never
// appends a reachability guess: the transport cause already states what failed.
func (e *ReplayTransportError) Error() string {
	if e.TimedOut() {
		return fmt.Sprintf(
			"replay: %s %s timed out after %d ms: %v",
			e.Method,
			e.URL,
			e.Budget.Milliseconds(),
			e.Err,
		)
	}
	return fmt.Sprintf("replay: %s %s: %v", e.Method, e.URL, e.Err)
}

// Unwrap exposes the transport cause.
func (e *ReplayTransportError) Unwrap() error { return e.Err }

// TimedOut reports whether the transport failure was a deadline or a network
// timeout.
func (e *ReplayTransportError) TimedOut() bool {
	if errors.Is(e.Err, context.DeadlineExceeded) || errors.Is(e.Err, context.Canceled) {
		return true
	}
	var timeout interface{ Timeout() bool }
	return errors.As(e.Err, &timeout) && timeout.Timeout()
}

// Replay resends one captured network entry as a real HTTP request through a Go
// client, which bypasses CORS and carries the page's cookies for the target host
// without writing anything back to the page. Target is a sequence number or a
// unique URL substring; an ambiguous target is refused with every candidate's
// sequence. Method and headers override the captured values, and Body replaces
// the captured request body when BodySet is set; otherwise the body is fetched
// from the browser, and a body it no longer retains is reported in
// BodyUncaptured rather than sent empty. CheckScope authorizes the target and
// every redirect before it is requested. Up to five redirects are followed and
// reported in Chain. The response body is capped at MaxBytes (256 KB ceiling)
// and both sensitive headers and text bodies are redacted unless the page
// enabled secrets capture.
func (p *Page) Replay(ctx context.Context, spec ReplaySpec) (ReplayResult, error) {
	entries, _ := p.NetworkEntries()
	entry, err := matchReplayEntry(entries, spec.Target)
	if err != nil {
		return ReplayResult{}, err
	}
	target, err := url.Parse(entry.URL)
	if err != nil {
		return ReplayResult{}, fmt.Errorf("replay: captured URL %q is not parseable: %w", entry.URL, err)
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return ReplayResult{}, fmt.Errorf(
			"replay: captured URL %s uses scheme %q — only http and https requests can be replayed",
			entry.URL,
			target.Scheme,
		)
	}
	method, err := replayMethod(entry.Method, spec.Method)
	if err != nil {
		return ReplayResult{}, err
	}
	if err := replayScope(spec, target.String()); err != nil {
		return ReplayResult{}, err
	}
	body, hasBody, bodyUncaptured, err := p.replayBody(ctx, entry, method, spec)
	if err != nil {
		return ReplayResult{}, err
	}
	headers := replayHeaders(entry.ReqHeaders, spec.Headers)
	jar, err := cookiejar.New(nil)
	if err != nil {
		return ReplayResult{}, fmt.Errorf("replay: create cookie jar: %w", err)
	}
	if headers.Get("Cookie") == "" {
		cookies, err := p.replayCookies(ctx, target)
		if err != nil {
			return ReplayResult{}, err
		}
		jar.SetCookies(target, cookies)
	}

	ctx, cancel := context.WithTimeout(ctx, replayTimeout)
	defer cancel()
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	keep := p.captureSecrets()
	limit := replayBodyLimit(spec.MaxBytes)
	start := time.Now()
	chain := make([]ReplayHop, 0, replayMaxRedirects+1)
	curURL, curMethod, curBody, curHasBody := target.String(), method, body, hasBody
	for hop := 0; ; hop++ {
		if hop > 0 {
			if err := replayScope(spec, curURL); err != nil {
				return ReplayResult{}, err
			}
		}
		var payload io.Reader
		if curHasBody {
			payload = strings.NewReader(curBody)
		}
		req, err := http.NewRequestWithContext(ctx, curMethod, curURL, payload)
		if err != nil {
			return ReplayResult{}, fmt.Errorf("replay: build %s %s: %w", curMethod, curURL, err)
		}
		req.Header = headers.Clone()
		resp, err := client.Do(req)
		if err != nil {
			return ReplayResult{}, &ReplayTransportError{
				Method: curMethod,
				URL:    curURL,
				Budget: replayTimeout,
				Err:    err,
			}
		}
		chain = append(chain, ReplayHop{Status: resp.StatusCode, URL: curURL})

		location := resp.Header.Get("Location")
		if !isReplayRedirect(resp.StatusCode) || location == "" || hop == replayMaxRedirects {
			result, err := replayResult(resp, chain, ReplaySent{
				Method:  method,
				URL:     target.String(),
				Headers: replayEchoHeaders(headers, keep),
			}, keep, start, limit)
			if err != nil {
				return ReplayResult{}, err
			}
			result.BodyUncaptured = bodyUncaptured
			return result, nil
		}
		next, err := resp.Request.URL.Parse(location)
		if err != nil {
			closeErr := resp.Body.Close()
			if closeErr != nil {
				return ReplayResult{}, fmt.Errorf("replay: close redirect body from %s: %w", curURL, closeErr)
			}
			return ReplayResult{}, fmt.Errorf("replay: redirect target %q from %s is not a URL: %w", location, curURL, err)
		}
		if resp.StatusCode == http.StatusMovedPermanently ||
			resp.StatusCode == http.StatusFound ||
			resp.StatusCode == http.StatusSeeOther {
			if curMethod != http.MethodGet && curMethod != http.MethodHead {
				curMethod, curBody, curHasBody = http.MethodGet, "", false
			}
		}
		if err := resp.Body.Close(); err != nil {
			return ReplayResult{}, fmt.Errorf("replay: close redirect body from %s: %w", curURL, err)
		}
		curURL = next.String()
	}
}

// matchReplayEntry picks the one captured entry named by target, which is either
// a sequence number or a substring of the request URL. An ambiguous substring is
// refused with every candidate's sequence, method and URL.
func matchReplayEntry(entries []NetworkEntry, target string) (NetworkEntry, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return NetworkEntry{}, fmt.Errorf(
			"replay requires target=<seq> or target=<url substring> (read page_fetch what=network first)",
		)
	}
	if seq, err := strconv.ParseInt(target, 10, 64); err == nil {
		for _, e := range entries {
			if e.Seq == seq {
				return e, nil
			}
		}
		return NetworkEntry{}, fmt.Errorf(
			"%w: no network entry with seq %d (read page_fetch what=network for the current seq values)",
			ErrReplayTargetNotFound,
			seq,
		)
	}

	matches := make([]NetworkEntry, 0, len(entries))
	for _, e := range entries {
		if strings.Contains(e.URL, target) {
			matches = append(matches, e)
		}
	}
	switch len(matches) {
	case 0:
		return NetworkEntry{}, fmt.Errorf(
			"%w: no network entry matches target %q (target is a seq number or a substring of the request URL; read page_fetch what=network first)",
			ErrReplayTargetNotFound,
			target,
		)
	case 1:
		return matches[0], nil
	default:
		lines := make([]string, len(matches))
		for i, e := range matches {
			lines[i] = fmt.Sprintf("seq=%d %s %s", e.Seq, e.Method, e.URL)
		}
		return NetworkEntry{}, fmt.Errorf(
			"%w: target %q matches %d network entries — pass a seq or a longer URL substring: %s",
			ErrReplayTargetAmbiguous,
			target,
			len(matches),
			strings.Join(lines, "; "),
		)
	}
}

// replayMethod resolves the method to send, uppercasing the captured value and
// refusing a blank override.
func replayMethod(captured, override string) (string, error) {
	if override == "" {
		return strings.ToUpper(captured), nil
	}
	method := strings.ToUpper(strings.TrimSpace(override))
	if method == "" {
		return "", fmt.Errorf("replay: method override is blank (pass e.g. PUT or PATCH)")
	}
	return method, nil
}

// replayBody returns the body to send: the override when set, otherwise the
// captured post data fetched on demand from the browser. The third result
// reports that no body was retained and none was supplied, so nothing was sent.
func (p *Page) replayBody(
	ctx context.Context,
	entry NetworkEntry,
	method string,
	spec ReplaySpec,
) (string, bool, bool, error) {
	if spec.BodySet {
		return spec.Body, true, false, nil
	}
	if method == http.MethodGet || method == http.MethodHead {
		return "", false, false, nil
	}
	if !replayCarriedBody(entry) {
		return "", false, true, nil
	}
	body, retained, err := p.requestPostData(ctx, entry)
	if err != nil {
		return "", false, false, err
	}
	if !retained {
		return "", false, true, nil
	}
	return body, true, false, nil
}

// replayCarriedBody reports whether the captured headers show a request body.
// Content-Type is the reliable signal because the capture may omit framing
// headers.
func replayCarriedBody(entry NetworkEntry) bool {
	if capturedHeader(entry.ReqHeaders, "Content-Type") != "" {
		return true
	}
	length := capturedHeader(entry.ReqHeaders, "Content-Length")
	return length != "" && length != "0"
}

// requestPostData fetches the captured request body on demand under the page
// lock, reporting false when the browser no longer retains it. Post data lives
// only as long as the request's network record, and multipart file parts are
// never included.
func (p *Page) requestPostData(ctx context.Context, entry NetworkEntry) (string, bool, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return "", false, fmt.Errorf("replay: page session is closed — reopen the page and retry")
	}
	res, err := proto.NetworkGetRequestPostData{RequestID: entry.requestID}.Call(p.Rod.Context(ctx))
	if err != nil {
		return "", false, nil
	}
	return res.PostData, true, nil
}

// replayScope runs the surface's scope check for rawURL and names the refused
// target. A nil check allows the request.
func replayScope(spec ReplaySpec, rawURL string) error {
	if spec.CheckScope == nil {
		return nil
	}
	if err := spec.CheckScope(rawURL); err != nil {
		return fmt.Errorf("replay: %s refused: %w", rawURL, err)
	}
	return nil
}

// replayCookies reads the page's cookies applicable to the target URL under the
// page lock and converts them for a private jar. Nothing is written back to the
// page.
func (p *Page) replayCookies(ctx context.Context, target *url.URL) ([]*http.Cookie, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return nil, fmt.Errorf("replay: page session is closed")
	}
	got, err := p.Rod.Context(ctx).Cookies([]string{target.String()})
	if err != nil {
		return nil, fmt.Errorf("replay: read page cookies for %s: %w", target.Host, err)
	}
	out := make([]*http.Cookie, 0, len(got))
	for _, c := range got {
		cookie := &http.Cookie{
			Name:     c.Name,
			Value:    c.Value,
			Path:     c.Path,
			Domain:   c.Domain,
			Secure:   c.Secure,
			HttpOnly: c.HTTPOnly,
		}
		if !c.Session && c.Expires > 0 {
			cookie.Expires = time.Unix(int64(c.Expires), 0)
		}
		out = append(out, cookie)
	}
	return out, nil
}

// replayHeaders copies the captured headers onto the outgoing request, dropping
// the ones the client owns and any value the capture redacted, then applies the
// per-header overrides.
func replayHeaders(captured, override map[string]string) http.Header {
	h := make(http.Header, len(captured)+len(override))
	for k, v := range captured {
		if replayDroppedHeaders[strings.ToLower(k)] || v == redactedValue {
			continue
		}
		h.Set(k, v)
	}
	for k, v := range override {
		h.Set(k, v)
	}
	return h
}

// replayEchoHeaders renders the outgoing headers for the result, redacting
// sensitive names unless the page enabled secrets capture.
func replayEchoHeaders(h http.Header, keep bool) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, vals := range h {
		if !keep && sensitiveHeaders[strings.ToLower(k)] {
			out[k] = redactedValue
			continue
		}
		out[k] = strings.Join(vals, ", ")
	}
	return out
}

// replayResponseHeaders copies the response headers, replacing sensitive values
// with the capture marker unless the page enabled secrets capture.
func replayResponseHeaders(h http.Header, keep bool) map[string][]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string][]string, len(h))
	for k, vals := range h {
		if !keep && sensitiveHeaders[strings.ToLower(k)] {
			out[k] = []string{redactedValue}
			continue
		}
		out[k] = append([]string(nil), vals...)
	}
	return out
}

// replayResult builds the final envelope from the last response. The response
// body is read and closed here, and text bodies are redacted unless the page
// enabled secrets capture.
func replayResult(
	resp *http.Response,
	chain []ReplayHop,
	sent ReplaySent,
	keep bool,
	start time.Time,
	limit int64,
) (ReplayResult, error) {
	payload, err := readReplayBody(resp, limit)
	if err != nil {
		return ReplayResult{}, fmt.Errorf("replay: read %s %s response: %w", resp.Request.Method, resp.Request.URL, err)
	}
	result := ReplayResult{
		Status:    resp.StatusCode,
		Headers:   replayResponseHeaders(resp.Header, keep),
		Body:      payload.Body,
		Base64:    payload.Base64,
		Bytes:     payload.Bytes,
		Truncated: payload.Truncated,
		MS:        time.Since(start).Milliseconds(),
		Request:   sent,
		Chain:     []ReplayHop{},
	}
	if len(chain) > 1 {
		result.Chain = chain
	}
	if result.Body != "" {
		result.Body, result.BodyRedactions = redactBody(result.Body, keep)
	}
	return result, nil
}

// replayBodyLimit resolves the body cap: zero or anything above the ceiling
// uses replayBodyCap.
func replayBodyLimit(requested int64) int64 {
	if requested <= 0 || requested > replayBodyCap {
		return replayBodyCap
	}
	return requested
}

// sensitiveBodySegments lists, lowercased, the key segments whose body values
// are replaced with redactedValue. A key is split on non-alphanumeric runes, so
// "sails.sid" matches "sid" while "consideration" does not.
var sensitiveBodySegments = map[string]bool{
	"cookie":        true,
	"cookies":       true,
	"authorization": true,
	"auth":          true,
	"token":         true,
	"session":       true,
	"sessionid":     true,
	"sessionkey":    true,
	"sid":           true,
	"wmsession":     true,
	"csrf":          true,
	"csrftoken":     true,
	"secret":        true,
	"password":      true,
	"passwd":        true,
	"apikey":        true,
}

// Body redaction patterns: JSON string fields, header-like lines, key=value
// pairs and auth credentials. They mirror the header redactor's key list and
// add the session-id and bearer-token shapes a body carries.
var (
	bodyJSONPair   = regexp.MustCompile(`"([^"\\\r\n]{1,80})"\s*:\s*"((?:[^"\\]|\\.)*)"`)
	bodyHeaderLine = regexp.MustCompile(`(?m)^[ \t]*([A-Za-z0-9_.\-]{1,80})[ \t]*:[ \t]*([^\r\n]+)$`)
	bodyKeyValue   = regexp.MustCompile(`(?i)\b([A-Za-z0-9_.\-]{1,80})=([^&;,\s"']+)`)
	bodyAuthToken  = regexp.MustCompile(`(?i)\b(Bearer|Basic)(\s+)([A-Za-z0-9._~+/=\-]{4,1000})`)
)

// redactBody replaces sensitive values in a text body while keeping their keys.
// It reports how many values it replaced. keep skips redaction entirely.
func redactBody(body string, keep bool) (string, int) {
	if keep || body == "" {
		return body, 0
	}
	count := 0
	body = redactBodyMatches(body, bodyJSONPair, 1, 2, isSensitiveBodyKey, &count)
	body = redactBodyMatches(body, bodyHeaderLine, 1, 2, isSensitiveBodyKey, &count)
	body = redactBodyMatches(body, bodyKeyValue, 1, 2, isSensitiveBodyKey, &count)
	body = redactBodyMatches(body, bodyAuthToken, 1, 3, alwaysSensitive, &count)
	return body, count
}

// redactBodyMatches rewrites every match whose key group is sensitive, keeping
// the text before and after the value group intact.
func redactBodyMatches(
	s string,
	re *regexp.Regexp,
	keyGroup, valueGroup int,
	sensitive func(string) bool,
	count *int,
) string {
	locs := re.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	last := 0
	for _, loc := range locs {
		b.WriteString(s[last:loc[0]])
		key := s[loc[2*keyGroup]:loc[2*keyGroup+1]]
		if sensitive(key) {
			vStart, vEnd := loc[2*valueGroup], loc[2*valueGroup+1]
			b.WriteString(s[loc[0]:vStart])
			b.WriteString(redactedValue)
			b.WriteString(s[vEnd:loc[1]])
			*count += 1
		} else {
			b.WriteString(s[loc[0]:loc[1]])
		}
		last = loc[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// isSensitiveBodyKey reports whether a body key holds a secret, comparing whole
// segments so a substring such as "sid" inside "consideration" never matches.
func isSensitiveBodyKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(strings.Trim(key, `"';`)))
	if sensitiveHeaders[k] {
		return true
	}
	for _, seg := range strings.FieldsFunc(k, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if sensitiveBodySegments[seg] {
			return true
		}
	}
	return false
}

// alwaysSensitive marks an auth credential value for redaction.
func alwaysSensitive(string) bool { return true }

// isReplayRedirect reports whether status is a redirect a replay follows.
func isReplayRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// replayPayload is the capped response body: Body holds UTF-8 text and Base64
// holds binary content, exclusive with Body.
type replayPayload struct {
	Body      string
	Base64    string
	Bytes     int64
	Truncated bool
}

// readReplayBody reads and closes a response body, capping it at limit and
// switching binary content to base64.
func readReplayBody(resp *http.Response, limit int64) (replayPayload, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	closeErr := resp.Body.Close()
	if err != nil {
		return replayPayload{}, fmt.Errorf("read body: %w", err)
	}
	if closeErr != nil {
		return replayPayload{}, fmt.Errorf("close body: %w", closeErr)
	}
	raw, truncated := capReplayBody(raw, limit)
	out := replayPayload{
		Bytes:     int64(len(raw)),
		Truncated: truncated,
	}
	if utf8.Valid(raw) {
		out.Body = string(raw)
		return out, nil
	}
	out.Base64 = base64.StdEncoding.EncodeToString(raw)
	return out, nil
}

// capReplayBody cuts raw to limit on a rune boundary, reporting whether it had
// to cut.
func capReplayBody(raw []byte, limit int64) ([]byte, bool) {
	if int64(len(raw)) <= limit {
		return raw, false
	}
	end := int(limit)
	for end > 0 && !utf8.RuneStart(raw[end]) {
		end--
	}
	return raw[:end], true
}

// capturedHeader returns the value of name from a captured header map,
// case-insensitively, or an empty string.
func capturedHeader(headers map[string]string, name string) string {
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// captureSecrets reports whether the page opted into keeping sensitive header
// values instead of the redaction marker.
func (p *Page) captureSecrets() bool {
	o := p.observer
	if o == nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.secrets
}

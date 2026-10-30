package pageops

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	"github.com/dakolli/webmcp/pkg/browsers"
)

const (
	// securityMaxItems bounds each DOM list the audit inspects (scripts, forms,
	// password fields) and the cookie set, so one huge page cannot dominate the
	// response. Omitted entries are counted in the report's truncated field.
	securityMaxItems = 200
	// securityEvidenceLimit bounds how many offending values one aggregated
	// check names in its evidence; the remainder is reported as "+N more".
	securityEvidenceLimit = 10
)

// securityProbeJS collects everything the audit reads from the DOM in one
// evaluation: script, form and password metadata plus the readable cookie set
// and any meta-delivered CSP. The lists are capped in the page so a page with
// thousands of nodes cannot make the probe payload unbounded.
const securityProbeJS = `() => {
  const abs = (u) => { try { return new URL(u, location.href).href } catch (e) { return u } };
  const scripts = [...document.querySelectorAll('script[src]')];
  const forms = [...document.querySelectorAll('form')];
  const passwords = [...document.querySelectorAll('input[type=password]')];
  const metaCSP = [...document.querySelectorAll('meta[http-equiv]')]
    .find(m => (m.getAttribute('http-equiv') || '').toLowerCase() === 'content-security-policy');
  return {
    url: location.href,
    scheme: location.protocol.replace(':', ''),
    cookies: document.cookie,
    meta_csp: metaCSP ? (metaCSP.getAttribute('content') || '') : '',
    script_total: scripts.length,
    scripts: scripts.slice(0, %d).map(s => {
      const src = s.getAttribute('src') || '';
      let cross = false;
      try { cross = new URL(src, location.href).origin !== location.origin } catch (e) {}
      return { src: abs(src), integrity: (s.getAttribute('integrity') || '').trim(), cross: cross };
    }),
    form_total: forms.length,
    forms: forms.slice(0, %d).map(f => {
      const raw = f.getAttribute('action') || '';
      return {
        action: raw ? abs(raw) : '',
        method: (f.getAttribute('method') || 'get').toLowerCase(),
        password: !!f.querySelector('input[type=password]'),
      };
    }),
    password_total: passwords.length,
    passwords: passwords.slice(0, %d).map(i => i.getAttribute('autocomplete') || ''),
  };
}`

// securityScript is one DOM script element with its resolved src, integrity
// hash and whether it is cross-origin.
type securityScript struct {
	// Src is the absolute script URL.
	Src string `json:"src"`
	// Integrity is the integrity attribute value, empty when absent.
	Integrity string `json:"integrity"`
	// Cross reports a script served from an origin other than the page's.
	Cross bool `json:"cross"`
}

// securityForm is one DOM form with its submission target resolved.
type securityForm struct {
	// Action is the absolute action URL, empty when the attribute is absent.
	Action string `json:"action"`
	// Method is the lowercased form method, defaulting to get.
	Method string `json:"method"`
	// Password reports a password field inside the form.
	Password bool `json:"password"`
}

// securityProbe is the payload one in-page evaluation returns.
type securityProbe struct {
	URL           string           `json:"url"`
	Scheme        string           `json:"scheme"`
	Cookies       string           `json:"cookies"`
	MetaCSP       string           `json:"meta_csp"`
	Scripts       []securityScript `json:"scripts"`
	ScriptTotal   int              `json:"script_total"`
	Forms         []securityForm   `json:"forms"`
	FormTotal     int              `json:"form_total"`
	Passwords     []string         `json:"passwords"`
	PasswordTotal int              `json:"password_total"`
}

// securityCheck is one passive finding: a stable id, a status of ok, warn, fail
// or info, the observed evidence and an actionable detail.
type securityCheck struct {
	// ID names the check (a stable identifier, e.g. "csp" or "cookie:session").
	ID string `json:"id"`
	// Status is ok, warn, fail or info.
	Status string `json:"status"`
	// Evidence is the observed value proving the status, empty when none exists.
	Evidence string `json:"evidence"`
	// Detail explains what the status means and what to do about it.
	Detail string `json:"detail"`
}

// securityReport is the JSON document PageSecurity returns.
type securityReport struct {
	// URL is the audited page address.
	URL string `json:"url"`
	// Policy is the effective Content-Security-Policy parsed for an agent.
	Policy securityPolicyInfo `json:"policy"`
	// Checks are the findings in a stable order.
	Checks []securityCheck `json:"checks"`
	// Violations is the page's bounded securitypolicyviolation ring.
	Violations securityViolations `json:"violations"`
	// Count is len(Checks).
	Count int `json:"count"`
	// Truncated counts DOM and cookie entries omitted by securityMaxItems, so a
	// cut is always explicit and never silent.
	Truncated int `json:"truncated"`
}

// securityPolicyInfo is the effective policy an agent needs to answer whether a
// payload can run: where the policy came from, its directives, and the script
// controls that decide inline and eval execution.
type securityPolicyInfo struct {
	// Present reports that the page declares a policy.
	Present bool `json:"present"`
	// Source is "header" or "meta", empty when no policy is present.
	Source string `json:"source"`
	// ReportOnly reports a policy delivered as Content-Security-Policy-Report-Only,
	// which reports violations and blocks nothing.
	ReportOnly bool `json:"report_only"`
	// Directives maps each directive to its source tokens, always non-nil.
	Directives map[string][]string `json:"directives"`
	// ScriptSrc is the declared script-src sources, empty when absent.
	ScriptSrc string `json:"script_src"`
	// StyleSrc is the declared style-src sources, empty when absent.
	StyleSrc string `json:"style_src"`
	// DefaultSrc is the declared default-src sources, empty when absent.
	DefaultSrc string `json:"default_src"`
	// UnsafeInline reports 'unsafe-inline' in the effective script-src, falling
	// back to default-src.
	UnsafeInline bool `json:"unsafe_inline"`
	// UnsafeEval reports 'unsafe-eval' in the effective script-src, falling back
	// to default-src.
	UnsafeEval bool `json:"unsafe_eval"`
	// Nonce is the script nonce, empty when the effective policy has none.
	Nonce string `json:"nonce"`
	// Hashes are the script hashes, always non-nil.
	Hashes []string `json:"hashes"`
}

// securityViolations is the CSP violation ring section: the buffered events
// oldest first, their count, and how many the engine's ring evicted.
type securityViolations struct {
	// Entries are the retained events, oldest first, never nil on the wire.
	Entries []browsers.CSPViolation `json:"entries"`
	// Count is len(Entries).
	Count int `json:"count"`
	// Dropped counts events the engine's 50-event ring evicted.
	Dropped int `json:"dropped"`
}

// PageSecurity returns a passive security audit of the live page as
// {"url","checks":[{id,status,evidence,detail}],"count","truncated"}. Every
// check reads truth the browser already holds — the audited document's own
// response headers (page-side capture, falling back to the network ring), the
// cookie jar for the current URL and one DOM evaluation — so the audit never
// reloads or probes the site. The ring spans navigations, so the ring-scoped
// checks start at the entry the engine marked as the current top-level document
// and ignore everything captured before it; it never clears the ring.
func PageSecurity(ctx context.Context, p *browsers.Page) (string, error) {
	ctx, done, err := beginPage(ctx, p, "security")
	if err != nil {
		return "", err
	}
	defer done()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	pc := p.Rod.Context(ctx)
	res, err := pc.Eval(fmt.Sprintf(securityProbeJS, securityMaxItems, securityMaxItems, securityMaxItems))
	if err != nil {
		return "", fmt.Errorf("evaluate security probe: %w", err)
	}
	var probe securityProbe
	if err := res.Value.Unmarshal(&probe); err != nil {
		return "", fmt.Errorf("decode security probe: %w", err)
	}

	ring, _ := p.NetworkEntries()
	doc, entries, found := securityDocumentRing(ring)
	enforcedCSP, reportOnlyCSP := p.DocumentCSP()
	headers, captured := documentHeaders(
		doc.RespHeaders,
		enforcedCSP,
		reportOnlyCSP,
		p.DocumentSecurityHeaders(),
	)
	found = found || captured
	cut := found && doc.RespHeadersTruncated && !captured
	cookies, cookiesOmitted := pageCookieChecks(pc, probe.URL, probe.Cookies)

	checks := securityHeaderChecks(probe, headers, found, cut)
	checks = append(checks, securityCSPBypassChecks(
		securityPolicy(probe, headers),
		headerValue(headers, "content-security-policy-report-only"),
	)...)
	checks = append(checks, cookies...)
	checks = append(checks, securityMixedContentCheck(probe, entries))
	checks = append(checks, securityContentChecks(probe)...)
	checks = append(checks, securityCORSCheck(headers))

	violations, violationsDropped := p.CSPViolations()
	report := securityReport{
		URL: probe.URL,
		Policy: effectiveSecurityPolicy(
			headerValue(headers, "content-security-policy"),
			strings.TrimSpace(probe.MetaCSP),
			headerValue(headers, "content-security-policy-report-only"),
		),
		Checks: checks,
		Violations: securityViolations{
			Entries: violations,
			Count:   len(violations),
			Dropped: violationsDropped,
		},
		Count:     len(checks),
		Truncated: securityOmitted(probe) + cookiesOmitted,
	}
	b, err := json.Marshal(report)
	if err != nil {
		return "", fmt.Errorf("encode security report: %w", err)
	}
	return string(b), nil
}

// securityOmitted counts the scripts, forms and password fields the probe left
// out because a list exceeded securityMaxItems.
func securityOmitted(probe securityProbe) int {
	omitted := 0
	for _, n := range []int{
		probe.ScriptTotal - len(probe.Scripts),
		probe.FormTotal - len(probe.Forms),
		probe.PasswordTotal - len(probe.Passwords),
	} {
		if n > 0 {
			omitted += n
		}
	}
	return omitted
}

// documentHeaders returns the audited document's response headers, preferring
// the page's uncapped capture over the network ring. The two CSP values and the
// stored security headers are replaced by the page-side record when it has one,
// so they survive clear_logs and are not clipped at 512 bytes; every other ring
// header keeps its clipped value. captured reports whether any stored value was
// applied, so the caller can stop describing the headers as absent or cut. The
// ring itself is never modified.
func documentHeaders(
	headers map[string]string,
	enforced,
	reportOnly string,
	stored map[string]string,
) (map[string]string, bool) {
	enforced = strings.TrimSpace(enforced)
	reportOnly = strings.TrimSpace(reportOnly)

	replaced := make(map[string]bool, len(stored)+2)
	if enforced != "" {
		replaced["content-security-policy"] = true
	}
	if reportOnly != "" {
		replaced["content-security-policy-report-only"] = true
	}
	for name, value := range stored {
		if strings.TrimSpace(value) != "" {
			replaced[strings.ToLower(name)] = true
		}
	}

	merged := make(map[string]string, len(headers)+len(replaced))
	for name, value := range headers {
		if replaced[strings.ToLower(name)] {
			continue
		}
		merged[name] = value
	}
	captured := false
	if enforced != "" {
		merged["content-security-policy"] = enforced
		captured = true
	}
	if reportOnly != "" {
		merged["content-security-policy-report-only"] = reportOnly
		captured = true
	}
	for name, value := range stored {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		merged[strings.ToLower(name)] = value
		captured = true
	}
	return merged, captured
}

// securityDocumentRing returns the entry the engine marked as the audited
// page's current top-level document, plus every request observed from it
// onward. The marker comes from the browser's frame and loader identity, so the
// boundary holds across navigations, a same-origin route change and a
// same-origin iframe without matching URLs. found is false when the document
// request is no longer in the ring; the callers then report empty evidence
// instead of borrowing another document's requests.
func securityDocumentRing(
	entries []browsers.NetworkEntry,
) (doc browsers.NetworkEntry, scoped []browsers.NetworkEntry, found bool) {
	for i := len(entries) - 1; i >= 0; i-- {
		if !entries[i].TopDocument {
			continue
		}
		return entries[i], entries[i:], true
	}
	return browsers.NetworkEntry{}, nil, false
}

// securityHeadersRead lists, lowercased, every response header the audit reads.
// Each must carry one value: the audit judges that value, so different values
// for one name are reported as a conflict rather than as a wrong value.
var securityHeadersRead = []string{
	"content-security-policy",
	"content-security-policy-report-only",
	"strict-transport-security",
	"x-frame-options",
	"x-content-type-options",
	"referrer-policy",
	"permissions-policy",
	"cross-origin-opener-policy",
	"access-control-allow-origin",
	"access-control-allow-credentials",
}

// splitHeaderValues splits one captured header value into the distinct values
// it carried, in wire order. Chrome joins repeated header fields with a
// newline, so equal copies collapse to one and differing copies stay separate.
func splitHeaderValues(raw string) []string {
	var values []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !slices.Contains(values, line) {
			values = append(values, line)
		}
	}
	return values
}

// matchingHeaderKeys returns the keys that name the header case-insensitively,
// sorted so a map carrying two casings resolves deterministically.
func matchingHeaderKeys(headers map[string]string, name string) []string {
	var keys []string
	for k := range headers {
		if strings.EqualFold(k, name) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// headerValues returns the distinct values the named header carried, merging
// case variants and collapsing equal copies. Differing copies keep wire order.
func headerValues(headers map[string]string, name string) []string {
	var values []string
	for _, k := range matchingHeaderKeys(headers, name) {
		for _, v := range splitHeaderValues(headers[k]) {
			if !slices.Contains(values, v) {
				values = append(values, v)
			}
		}
	}
	return values
}

// headerValue returns the value the audit judges for name: the only value a
// repeated equal header carried, or the first value when the copies differ.
// securityHeaderConflictChecks names the disagreement separately, so no check
// ever sees the newline-joined string Chrome produces for a repeated header.
func headerValue(headers map[string]string, name string) string {
	values := headerValues(headers, name)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// securityHeaderConflictChecks reports every header the audit reads that
// arrived with different values, naming all of them. It returns nothing when
// no header conflicts, so an ordinary response keeps its check count.
func securityHeaderConflictChecks(headers map[string]string) []securityCheck {
	var offenders []string
	for _, name := range securityHeadersRead {
		values := headerValues(headers, name)
		if len(values) < 2 {
			continue
		}
		offenders = append(offenders, name+": "+strings.Join(values, " | "))
	}
	if len(offenders) == 0 {
		return nil
	}
	return []securityCheck{securityFinding(
		"header_conflict",
		"every header the audit reads carries one value",
		"%d header(s) that must hold one value arrived with different values; send one value, or repeat the same value — the audit reads the first",
		offenders,
	)}
}

// hstsDisabled reports whether a Strict-Transport-Security value sets max-age=0,
// which is the standard way to switch HSTS off.
func hstsDisabled(value string) bool {
	for _, part := range strings.Split(value, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "max-age") {
			continue
		}
		if strings.Trim(strings.TrimSpace(v), `"`) == "0" {
			return true
		}
	}
	return false
}

// securityHeaderChecks audits the response security headers and the CSP,
// including a meta-delivered policy when the header is absent.
func securityHeaderChecks(
	probe securityProbe,
	headers map[string]string,
	found,
	cut bool,
) []securityCheck {
	checks := make([]securityCheck, 0, 11)
	switch {
	case !found:
		checks = append(checks, securityCheck{
			ID:     "headers",
			Status: "info",
			Detail: "the audited document's response is not in the network ring (newest 200 requests, spanning navigations), so the other headers below read as absent — the CSP policy is still reported from the page's own capture; re-open the page to audit the rest",
		})
	case cut:
		checks = append(checks, securityCheck{
			ID:     "headers",
			Status: "info",
			Detail: "the captured response headers were truncated at 32 headers / 4 KB, so a header reported absent below may have been dropped at capture",
		})
	}

	checks = append(checks, securityHeaderConflictChecks(headers)...)

	csp := headerValue(headers, "content-security-policy")
	metaCSP := strings.TrimSpace(probe.MetaCSP)
	switch {
	case csp != "":
		checks = append(checks, securityCheck{
			ID:       "csp",
			Status:   "ok",
			Evidence: csp,
			Detail:   "a Content-Security-Policy header is set",
		})
	case metaCSP != "":
		checks = append(checks, securityCheck{
			ID:       "csp",
			Status:   "warn",
			Evidence: metaCSP,
			Detail:   "Content-Security-Policy is delivered through <meta http-equiv>, which browsers ignore for frame-ancestors and report-uri — send it as a header",
		})
	default:
		checks = append(checks, securityCheck{
			ID:     "csp",
			Status: "warn",
			Detail: "no Content-Security-Policy header or <meta http-equiv=content-security-policy> — add one to constrain script and style sources",
		})
	}

	policy := csp
	if policy == "" {
		policy = metaCSP
	}
	if policy != "" {
		parsed := parseCSP(policy)
		checks = append(checks,
			weakScriptSourceCheck(parsed, "csp_unsafe_inline", "'unsafe-inline'"),
			weakScriptSourceCheck(parsed, "csp_unsafe_eval", "'unsafe-eval'"),
		)
	}

	checks = append(checks, securityHSTSCheck(probe.Scheme, headerValue(headers, "strict-transport-security")))

	xfo := headerValue(headers, "x-frame-options")
	frameAncestors := strings.Contains(strings.ToLower(policy), "frame-ancestors")
	switch {
	case xfo != "":
		checks = append(checks, securityCheck{
			ID:       "x_frame_options",
			Status:   "ok",
			Evidence: xfo,
			Detail:   "X-Frame-Options is set",
		})
	case frameAncestors:
		checks = append(checks, securityCheck{
			ID:       "x_frame_options",
			Status:   "ok",
			Evidence: "Content-Security-Policy frame-ancestors",
			Detail:   "X-Frame-Options is absent but the CSP frame-ancestors directive controls framing",
		})
	default:
		checks = append(checks, securityCheck{
			ID:     "x_frame_options",
			Status: "warn",
			Detail: "no X-Frame-Options header and no CSP frame-ancestors directive — the page can be framed for clickjacking",
		})
	}

	xctoValues := headerValues(headers, "x-content-type-options")
	xcto := strings.TrimSpace(headerValue(headers, "x-content-type-options"))
	switch {
	case len(xctoValues) > 1:
		checks = append(checks, securityCheck{
			ID:       "x_content_type_options",
			Status:   "info",
			Evidence: strings.Join(xctoValues, " | "),
			Detail:   "X-Content-Type-Options arrived with different values, so no single value can be judged; header_conflict names them",
		})
	case strings.EqualFold(xcto, "nosniff"):
		checks = append(checks, securityCheck{
			ID:       "x_content_type_options",
			Status:   "ok",
			Evidence: xcto,
			Detail:   "X-Content-Type-Options is nosniff",
		})
	default:
		detail := "X-Content-Type-Options is absent — add nosniff so browsers stop sniffing scripts and styles"
		if xcto != "" {
			detail = "X-Content-Type-Options is " + xcto + " — it must be nosniff"
		}
		checks = append(checks, securityCheck{
			ID:       "x_content_type_options",
			Status:   "warn",
			Evidence: xcto,
			Detail:   detail,
		})
	}

	for _, opt := range []struct {
		id     string
		header string
		detail string
	}{
		{"referrer_policy", "referrer-policy", "recommended value: strict-origin-when-cross-origin"},
		{"permissions_policy", "permissions-policy", "recommended: disable features the page does not use"},
		{"coop", "cross-origin-opener-policy", "recommended value: same-origin for pages that open cross-origin windows"},
	} {
		value := headerValue(headers, opt.header)
		check := securityCheck{
			ID:       opt.id,
			Status:   "info",
			Detail:   "no " + opt.header + " header; " + opt.detail,
			Evidence: value,
		}
		if value != "" {
			check.Status = "ok"
			check.Detail = opt.header + " is set"
		}
		checks = append(checks, check)
	}
	return checks
}

// weakScriptSourceCheck judges whether the effective script directive of p
// allows token. Evidence quotes the directive that was judged on both the
// passing and the warning result, so a pass is never reported without the text
// that produced it.
func weakScriptSourceCheck(p cspPolicy, id, token string) securityCheck {
	d, ok := p.effective("script-src")
	if !ok {
		return securityCheck{
			ID:       id,
			Status:   "info",
			Evidence: p.raw,
			Detail:   "the policy has no script-src or default-src directive, so " + token + " is not judged",
		}
	}
	if hasSource(d.values, token) {
		return securityCheck{
			ID:       id,
			Status:   "warn",
			Evidence: d.text,
			Detail:   d.name + " allows " + token + " — use nonces or hashes for scripts and avoid eval-style code",
		}
	}
	return securityCheck{
		ID:       id,
		Status:   "ok",
		Evidence: d.text,
		Detail:   "the effective script directive does not allow " + token,
	}
}

// securityPolicy returns the enforced policy text, preferring the response
// header over a meta-delivered policy.
func securityPolicy(probe securityProbe, headers map[string]string) string {
	if csp := headerValue(headers, "content-security-policy"); csp != "" {
		return csp
	}
	return strings.TrimSpace(probe.MetaCSP)
}

// effectiveSecurityPolicy parses the enforced policy, preferring a
// Content-Security-Policy header over a <meta http-equiv> form and falling back
// to a Content-Security-Policy-Report-Only header when neither is present. A
// report-only policy is parsed and flagged, because it still reports violations
// and still tells an agent what the site intends to block.
func effectiveSecurityPolicy(header, meta, reportOnly string) securityPolicyInfo {
	info := securityPolicyInfo{
		Directives: map[string][]string{},
		Hashes:     []string{},
	}
	raw := strings.TrimSpace(header)
	source := "header"
	if raw == "" {
		if m := strings.TrimSpace(meta); m != "" {
			raw, source = m, "meta"
		}
	}
	if raw == "" {
		if ro := strings.TrimSpace(reportOnly); ro != "" {
			raw, source = ro, "header"
			info.ReportOnly = true
		}
	}
	if raw == "" {
		return info
	}
	info.Present = true
	info.Source = source

	p := parseCSP(raw)
	for _, d := range p.directives {
		info.Directives[d.name] = cspSources(d.text)
	}
	if d, ok := p.directive("script-src"); ok {
		info.ScriptSrc = strings.Join(cspSources(d.text), " ")
	}
	if d, ok := p.directive("style-src"); ok {
		info.StyleSrc = strings.Join(cspSources(d.text), " ")
	}
	if d, ok := p.directive("default-src"); ok {
		info.DefaultSrc = strings.Join(cspSources(d.text), " ")
	}

	eff, ok := p.effective("script-src")
	if !ok {
		return info
	}
	sources := cspSources(eff.text)
	info.UnsafeInline = hasSource(eff.values, "'unsafe-inline'")
	info.UnsafeEval = hasSource(eff.values, "'unsafe-eval'")
	info.Nonce = cspNonce(sources)
	info.Hashes = cspHashes(sources)
	return info
}

// cspSources returns the source tokens of one directive text, preserving their
// case so a nonce or hash is never mangled. The result is never nil.
func cspSources(text string) []string {
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return []string{}
	}
	return fields[1:]
}

// cspNonce extracts the first 'nonce-...' token from a source list, preserving
// the nonce's case.
func cspNonce(sources []string) string {
	const prefix = "'nonce-"
	for _, s := range sources {
		if len(s) <= len(prefix) || !strings.HasSuffix(s, "'") {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(s), prefix) {
			continue
		}
		return s[len(prefix) : len(s)-1]
	}
	return ""
}

// cspHashes returns the sha256/sha384/sha512 tokens from a source list,
// preserving their case. The result is never nil.
func cspHashes(sources []string) []string {
	hashes := []string{}
	for _, s := range sources {
		token := strings.Trim(s, "'")
		low := strings.ToLower(token)
		if strings.HasPrefix(low, "sha256-") ||
			strings.HasPrefix(low, "sha384-") ||
			strings.HasPrefix(low, "sha512-") {
			hashes = append(hashes, token)
		}
	}
	return hashes
}

// cspBypassHost is a script-src host that cannot be trusted as a script source
// because it serves attacker-controlled code through JSONP or user hosting.
type cspBypassHost struct {
	// suffix is the registrable domain, matched together with its subdomains.
	suffix string
	// reason explains the known bypass pattern for the domain.
	reason string
}

// cspBypassHosts is the closed set of hosts the bypass probe flags. Every entry
// serves arbitrary script to anyone, so trusting it in script-src is close to
// trusting '*'.
var cspBypassHosts = []cspBypassHost{
	{"google.com", "Google JSONP endpoints and the /url open redirect"},
	{"googleapis.com", "Google-hosted JSONP endpoints"},
	{"cloudflare.com", "cdnjs serves arbitrary third-party scripts"},
	{"jsdelivr.net", "serves arbitrary npm and GitHub content"},
	{"unpkg.com", "serves arbitrary npm content"},
	{"github.io", "user-controlled GitHub Pages"},
	{"herokuapp.com", "user-controlled apps"},
	{"firebaseapp.com", "user-controlled hosting"},
	{"netlify.app", "user-controlled sites"},
	{"vercel.app", "user-controlled sites"},
	{"s3.amazonaws.com", "user-controlled buckets"},
	{"cloudfront.net", "user-controlled distributions"},
	{"azureedge.net", "user-controlled CDN endpoints"},
	{"azurewebsites.net", "user-controlled apps"},
	{"appspot.com", "user-controlled apps"},
}

// cspDirective is one parsed CSP directive: its lowercased name, the original
// trimmed text used as evidence and the lowercased source tokens.
type cspDirective struct {
	name   string
	text   string
	values []string
}

// cspPolicy is a parsed Content-Security-Policy, preserving the first
// occurrence of each directive as browsers ignore later duplicates.
type cspPolicy struct {
	raw        string
	directives []cspDirective
}

// parseCSP parses a policy string into ordered directives.
func parseCSP(raw string) cspPolicy {
	p := cspPolicy{raw: strings.TrimSpace(raw)}
	seen := make(map[string]bool)
	for _, part := range strings.Split(raw, ";") {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		name := strings.ToLower(fields[0])
		if seen[name] {
			continue
		}
		seen[name] = true
		values := make([]string, 0, len(fields)-1)
		for _, field := range fields[1:] {
			values = append(values, strings.ToLower(field))
		}
		p.directives = append(p.directives, cspDirective{
			name:   name,
			text:   strings.TrimSpace(part),
			values: values,
		})
	}
	return p
}

// directive returns the named directive.
func (p cspPolicy) directive(name string) (cspDirective, bool) {
	for _, d := range p.directives {
		if d.name == name {
			return d, true
		}
	}
	return cspDirective{}, false
}

// effective returns the named directive, falling back to default-src when the
// named directive is absent.
func (p cspPolicy) effective(name string) (cspDirective, bool) {
	if d, ok := p.directive(name); ok {
		return d, true
	}
	return p.directive("default-src")
}

// hasSource reports whether a source list contains the exact token.
func hasSource(values []string, token string) bool {
	for _, v := range values {
		if v == token {
			return true
		}
	}
	return false
}

// hasNonceOrHash reports whether a script source list carries a nonce or hash,
// which makes CSP3 ignore a co-listed 'unsafe-inline'.
func hasNonceOrHash(values []string) bool {
	for _, v := range values {
		token := strings.Trim(v, "'")
		if strings.HasPrefix(token, "nonce-") || strings.HasPrefix(token, "sha256-") ||
			strings.HasPrefix(token, "sha384-") || strings.HasPrefix(token, "sha512-") {
			return true
		}
	}
	return false
}

// cspSourceHost extracts the host from one CSP source token, returning an empty
// string for keywords, nonces, hashes, schemes and unparseable tokens.
func cspSourceHost(token string) string {
	if token == "" || strings.HasPrefix(token, "'") || strings.HasSuffix(token, ":") {
		return ""
	}
	host := token
	if _, rest, ok := strings.Cut(host, "://"); ok {
		host = rest
	}
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	host = strings.TrimPrefix(host, "*.")
	if host == "" || strings.Contains(host, "*") {
		return ""
	}
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	return strings.ToLower(host)
}

// cspBypassHostFor matches a host against the known bypass set, including its
// subdomains, and returns the reason for the match.
func cspBypassHostFor(host string) (string, bool) {
	for _, b := range cspBypassHosts {
		if host == b.suffix || strings.HasSuffix(host, "."+b.suffix) {
			return b.reason, true
		}
	}
	return "", false
}

// cspNoPolicyDetail explains an info probe when the page declares no policy.
const cspNoPolicyDetail = "no Content-Security-Policy is set, so there is no policy to bypass"

// securityCSPBypassChecks statically probes the enforced policy for constructs
// that let an attacker defeat it. It never injects, executes or loads anything:
// every result is derived from the policy text alone, and evidence quotes a
// fragment of the real header or meta tag.
func securityCSPBypassChecks(policy, reportOnly string) []securityCheck {
	p := parseCSP(policy)
	return []securityCheck{
		cspUnsafeInlineCheck(p),
		cspUnsafeEvalCheck(p),
		cspScriptWildcardCheck(p),
		cspObjectSrcCheck(p),
		cspBaseURICheck(p),
		cspFrameAncestorsCheck(p),
		cspReportOnlyCheck(reportOnly),
		cspTrustedHostCheck(p),
	}
}

// cspUnsafeInlineCheck reports whether the effective script directive lets an
// injected inline <script> run.
func cspUnsafeInlineCheck(p cspPolicy) securityCheck {
	const id = "csp_bypass_unsafe_inline"
	if p.raw == "" {
		return securityCheck{ID: id, Status: "info", Detail: cspNoPolicyDetail}
	}
	d, ok := p.effective("script-src")
	if !ok {
		return securityCheck{
			ID:     id,
			Status: "info",
			Detail: "the policy has no script-src or default-src directive, so inline scripts are unrestricted",
		}
	}
	if !hasSource(d.values, "'unsafe-inline'") {
		return securityCheck{
			ID:       id,
			Status:   "ok",
			Evidence: d.text,
			Detail:   "the effective script directive does not allow 'unsafe-inline'",
		}
	}
	if hasNonceOrHash(d.values) {
		return securityCheck{
			ID:       id,
			Status:   "ok",
			Evidence: d.text,
			Detail:   "'unsafe-inline' is present but a nonce or hash makes CSP3 ignore it for script elements",
		}
	}
	return securityCheck{
		ID:       id,
		Status:   "fail",
		Evidence: d.text,
		Detail:   "'unsafe-inline' lets any injected inline <script> execute; replace it with a nonce or hash",
	}
}

// cspUnsafeEvalCheck reports whether the effective script directive allows eval
// and Function-string execution.
func cspUnsafeEvalCheck(p cspPolicy) securityCheck {
	const id = "csp_bypass_unsafe_eval"
	if p.raw == "" {
		return securityCheck{ID: id, Status: "info", Detail: cspNoPolicyDetail}
	}
	d, ok := p.effective("script-src")
	if !ok {
		return securityCheck{
			ID:     id,
			Status: "info",
			Detail: "the policy has no script-src or default-src directive, so eval is unrestricted",
		}
	}
	if !hasSource(d.values, "'unsafe-eval'") {
		return securityCheck{
			ID:       id,
			Status:   "ok",
			Evidence: d.text,
			Detail:   "the effective script directive does not allow 'unsafe-eval'",
		}
	}
	return securityCheck{
		ID:       id,
		Status:   "fail",
		Evidence: d.text,
		Detail:   "'unsafe-eval' lets eval() and new Function() run injected strings; remove it and refactor eval-style code",
	}
}

// cspScriptWildcardCheck reports wildcard, scheme and subdomain-wildcard script
// sources, which let an attacker choose the script.
func cspScriptWildcardCheck(p cspPolicy) securityCheck {
	const id = "csp_bypass_script_wildcard"
	if p.raw == "" {
		return securityCheck{ID: id, Status: "info", Detail: cspNoPolicyDetail}
	}
	d, ok := p.effective("script-src")
	if !ok {
		return securityCheck{
			ID:     id,
			Status: "info",
			Detail: "the policy has no script-src or default-src directive, so any script host is allowed",
		}
	}
	var broad, scoped []string
	for _, v := range d.values {
		switch v {
		case "*", "http:", "https:", "data:", "blob:", "filesystem:":
			broad = append(broad, v)
		default:
			if strings.HasPrefix(v, "*.") {
				scoped = append(scoped, v)
			}
		}
	}
	if len(broad) > 0 {
		return securityCheck{
			ID:       id,
			Status:   "fail",
			Evidence: d.text,
			Detail: "script-src accepts any script from " + strings.Join(broad, ", ") +
				"; a wildcard or scheme source lets an attacker load arbitrary script — list exact hosts or use a nonce",
		}
	}
	if len(scoped) > 0 {
		return securityCheck{
			ID:       id,
			Status:   "warn",
			Evidence: d.text,
			Detail: "script-src trusts every subdomain of " + strings.Join(scoped, ", ") +
				"; one compromised subdomain serves arbitrary script — list the exact host",
		}
	}
	return securityCheck{
		ID:       id,
		Status:   "ok",
		Evidence: d.text,
		Detail:   "script-src uses no wildcard or scheme source",
	}
}

// cspObjectSrcCheck reports a policy with no object-src and no default-src
// fallback, leaving plugin content unrestricted.
func cspObjectSrcCheck(p cspPolicy) securityCheck {
	const id = "csp_bypass_object_src"
	if p.raw == "" {
		return securityCheck{ID: id, Status: "info", Detail: cspNoPolicyDetail}
	}
	if d, ok := p.effective("object-src"); ok {
		return securityCheck{
			ID:       id,
			Status:   "ok",
			Evidence: d.text,
			Detail:   "object-src is set directly or through default-src",
		}
	}
	return securityCheck{
		ID:     id,
		Status: "warn",
		Detail: "no object-src directive and no default-src fallback — plugin and <object> content is unrestricted; set object-src 'none'",
	}
}

// cspBaseURICheck reports a policy with no base-uri directive, which lets an
// injected <base> tag redirect relative URLs.
func cspBaseURICheck(p cspPolicy) securityCheck {
	const id = "csp_bypass_base_uri"
	if p.raw == "" {
		return securityCheck{ID: id, Status: "info", Detail: cspNoPolicyDetail}
	}
	if d, ok := p.directive("base-uri"); ok {
		return securityCheck{
			ID:       id,
			Status:   "ok",
			Evidence: d.text,
			Detail:   "base-uri is set",
		}
	}
	return securityCheck{
		ID:     id,
		Status: "warn",
		Detail: "no base-uri directive — an injected <base> tag can redirect relative script and form URLs; set base-uri 'self' or 'none'",
	}
}

// cspFrameAncestorsCheck reports a frame-ancestors directive that accepts any
// origin, which permits clickjacking.
func cspFrameAncestorsCheck(p cspPolicy) securityCheck {
	const id = "csp_bypass_frame_ancestors"
	if p.raw == "" {
		return securityCheck{ID: id, Status: "info", Detail: cspNoPolicyDetail}
	}
	d, ok := p.directive("frame-ancestors")
	if !ok {
		return securityCheck{
			ID:     id,
			Status: "info",
			Detail: "no frame-ancestors directive; framing is unrestricted and X-Frame-Options is the only remaining control",
		}
	}
	for _, v := range d.values {
		switch v {
		case "*", "http:", "https:":
			return securityCheck{
				ID:       id,
				Status:   "warn",
				Evidence: d.text,
				Detail: "frame-ancestors accepts " + v +
					", so any origin can frame the page for clickjacking; list the exact parent origins or use 'none'",
			}
		}
	}
	return securityCheck{
		ID:       id,
		Status:   "ok",
		Evidence: d.text,
		Detail:   "frame-ancestors limits which origins may frame the page",
	}
}

// cspReportOnlyCheck flags a report-only policy: it reports violations but
// blocks nothing, so an otherwise strict policy is not enforced.
func cspReportOnlyCheck(reportOnly string) securityCheck {
	const id = "csp_bypass_report_only"
	raw := strings.TrimSpace(reportOnly)
	if raw == "" {
		return securityCheck{ID: id, Status: "info", Detail: "no Content-Security-Policy-Report-Only header"}
	}
	p := parseCSP(raw)
	evidence := ""
	if d, ok := p.directive("script-src"); ok {
		evidence = d.text
	} else if len(p.directives) > 0 {
		evidence = p.directives[0].text
	}
	detail := "the policy is delivered as Content-Security-Policy-Report-Only, so violations are reported and nothing is blocked"
	if d, ok := p.effective("script-src"); ok && !hasSource(d.values, "'unsafe-inline'") {
		detail += "; this policy would otherwise block inline scripts — send it as Content-Security-Policy once the reports are clean"
	}
	return securityCheck{ID: id, Status: "info", Evidence: evidence, Detail: detail}
}

// cspTrustedHostCheck reports script-src hosts known to serve arbitrary script
// through JSONP or user-controlled hosting.
func cspTrustedHostCheck(p cspPolicy) securityCheck {
	const id = "csp_bypass_trusted_host"
	if p.raw == "" {
		return securityCheck{ID: id, Status: "info", Detail: cspNoPolicyDetail}
	}
	d, ok := p.effective("script-src")
	if !ok {
		return securityCheck{
			ID:     id,
			Status: "info",
			Detail: "the policy has no script-src or default-src directive, so every host is trusted",
		}
	}
	var hits, reasons []string
	seenHost := make(map[string]bool)
	seenReason := make(map[string]bool)
	for _, v := range d.values {
		host := cspSourceHost(v)
		if host == "" {
			continue
		}
		reason, matched := cspBypassHostFor(host)
		if !matched {
			continue
		}
		if !seenHost[v] {
			seenHost[v] = true
			hits = append(hits, v)
		}
		if !seenReason[reason] {
			seenReason[reason] = true
			reasons = append(reasons, reason)
		}
	}
	check := securityFinding(
		id,
		"no script-src host matches a known JSONP or open-redirect bypass host",
		"%d script-src host(s) serve arbitrary script through JSONP or user-controlled hosting; remove them or pin the policy with a nonce or hash",
		hits,
	)
	if check.Status == "warn" {
		check.Detail += " (" + strings.Join(reasons, "; ") + ")"
	} else {
		check.Evidence = d.text
	}
	return check
}

// securityHSTSCheck audits Strict-Transport-Security, treating any non-https
// page as a failure because HSTS is ignored on plain http.
func securityHSTSCheck(scheme, value string) securityCheck {
	switch {
	case scheme != "https":
		return securityCheck{
			ID:       "hsts",
			Status:   "fail",
			Evidence: value,
			Detail:   "the page is served over plain http; HSTS only applies to https responses — serve the site over https and send Strict-Transport-Security",
		}
	case value == "":
		return securityCheck{
			ID:     "hsts",
			Status: "fail",
			Detail: "no Strict-Transport-Security header — add max-age with includeSubDomains so browsers never downgrade to http",
		}
	case hstsDisabled(value):
		return securityCheck{
			ID:       "hsts",
			Status:   "warn",
			Evidence: value,
			Detail:   "Strict-Transport-Security sets max-age=0, which disables HSTS",
		}
	default:
		return securityCheck{
			ID:       "hsts",
			Status:   "ok",
			Evidence: value,
			Detail:   "Strict-Transport-Security is set",
		}
	}
}

// pageCookieChecks audits the flags of every cookie the browser would send to
// pageURL, sorted by name and domain so two audits emit the same order. readable
// is the document.cookie fallback used when the cookie API is unavailable;
// omitted counts cookies cut by securityMaxItems.
func pageCookieChecks(pc *rod.Page, pageURL, readable string) (checks []securityCheck, omitted int) {
	res, err := proto.NetworkGetCookies{Urls: []string{pageURL}}.Call(pc)
	if err != nil {
		return readableCookieChecks(readable, err), 0
	}
	cookies := res.Cookies
	sort.Slice(cookies, func(i, j int) bool {
		if cookies[i].Name != cookies[j].Name {
			return cookies[i].Name < cookies[j].Name
		}
		return cookies[i].Domain < cookies[j].Domain
	})
	if len(cookies) > securityMaxItems {
		omitted = len(cookies) - securityMaxItems
		cookies = cookies[:securityMaxItems]
	}
	if len(cookies) == 0 {
		return []securityCheck{{
			ID:     "cookies",
			Status: "ok",
			Detail: "no cookies are set for this URL",
		}}, 0
	}

	checks = make([]securityCheck, 0, 2*len(cookies))
	for _, c := range cookies {
		checks = append(checks, securityCookieChecks(c)...)
	}
	return checks, omitted
}

// securityCookieChecks audits one cookie, returning its HttpOnly/Secure finding
// followed by its SameSite finding. The two are always emitted in that order so
// a cookie set never changes the check count or order between audits.
func securityCookieChecks(c *proto.NetworkCookie) []securityCheck {
	var missing []string
	if !c.HTTPOnly {
		missing = append(missing, "HttpOnly")
	}
	if !c.Secure {
		missing = append(missing, "Secure")
	}
	flags := securityCheck{
		ID:       "cookie:" + c.Name,
		Status:   "ok",
		Evidence: fmt.Sprintf("%s (domain=%s) HttpOnly=%t Secure=%t SameSite=%q", c.Name, c.Domain, c.HTTPOnly, c.Secure, string(c.SameSite)),
		Detail:   "HttpOnly and Secure are set",
	}
	if len(missing) > 0 {
		flags.Status = "warn"
		flags.Detail = "missing cookie flags: " + strings.Join(missing, ", ") +
			" — set HttpOnly so scripts cannot read the cookie and Secure so it is never sent over http"
	}
	return []securityCheck{flags, securitySameSiteCheck(c.Name, string(c.SameSite))}
}

// securitySameSiteCheck names the SameSite state the cookie actually carries. An
// empty value means the attribute is absent, which is not the same as an explicit
// None; None gets its own finding because the browser sends that cookie on
// cross-site requests. An unrecognised value is reported raw, never as missing.
func securitySameSiteCheck(name, value string) securityCheck {
	base := "cookie:" + name + ":samesite"
	switch {
	case value == "":
		return securityCheck{
			ID:     base + "_missing",
			Status: "warn",
			Detail: "missing SameSite — browsers then apply their own default, so the site does not control cross-site sends; set SameSite=Lax or Strict",
		}
	case strings.EqualFold(value, string(proto.NetworkCookieSameSiteNone)):
		return securityCheck{
			ID:       base + "_none",
			Status:   "warn",
			Evidence: "SameSite=" + value,
			Detail:   "SameSite=None sends the cookie on cross-site requests, so any origin the browser loads can carry it; use Lax or Strict unless the cookie is genuinely needed cross-site",
		}
	case strings.EqualFold(value, string(proto.NetworkCookieSameSiteLax)):
		return securityCheck{
			ID:       base + "_lax",
			Status:   "ok",
			Evidence: "SameSite=" + value,
			Detail:   "SameSite=Lax withholds the cookie from cross-site subrequests and most cross-site navigations",
		}
	case strings.EqualFold(value, string(proto.NetworkCookieSameSiteStrict)):
		return securityCheck{
			ID:       base + "_strict",
			Status:   "ok",
			Evidence: "SameSite=" + value,
			Detail:   "SameSite=Strict withholds the cookie from every cross-site request",
		}
	default:
		return securityCheck{
			ID:       base + "_unknown",
			Status:   "warn",
			Evidence: "SameSite=" + value,
			Detail:   "SameSite=" + value + " is not a value browsers recognise, so the cookie's cross-site behavior is undefined; send Lax, Strict or None",
		}
	}
}

// readableCookieChecks falls back to the document.cookie names when the cookie
// API fails: a readable cookie is proof that HttpOnly is not set.
func readableCookieChecks(readable string, err error) []securityCheck {
	var checks []securityCheck
	for _, part := range strings.Split(readable, ";") {
		name, _, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || name == "" {
			continue
		}
		checks = append(checks, securityCheck{
			ID:       "cookie:" + name,
			Status:   "warn",
			Evidence: name + " (readable via document.cookie)",
			Detail:   "the cookie is readable from JavaScript, so HttpOnly is not set; the cookie API was unavailable: " + err.Error(),
		})
	}
	if len(checks) == 0 {
		checks = append(checks, securityCheck{
			ID:     "cookies",
			Status: "info",
			Detail: "cookie flags are unavailable and no script-readable cookies exist: " + err.Error(),
		})
	}
	return checks
}

// securityMixedContentCheck flags plain-http requests the audited document made
// from an https page. entries must already be scoped to that document.
func securityMixedContentCheck(probe securityProbe, entries []browsers.NetworkEntry) securityCheck {
	if probe.Scheme != "https" {
		return securityCheck{
			ID:     "mixed_content",
			Status: "info",
			Detail: "the page is not served over https, so mixed-content blocking does not apply",
		}
	}
	var offenders []string
	for _, e := range entries {
		if strings.HasPrefix(strings.ToLower(e.URL), "http://") {
			offenders = append(offenders, e.URL)
		}
	}
	return securityFinding(
		"mixed_content",
		"no http:// subresources were observed for this document",
		"%d request(s) used plain http on an https page (blocked or downgraded by the browser); load every subresource over https",
		offenders,
	)
}

// securityContentChecks audits script integrity, form submission targets and
// password-field autocomplete from the DOM probe.
func securityContentChecks(probe securityProbe) []securityCheck {
	var sri []string
	crossOrigin := 0
	for _, s := range probe.Scripts {
		if !s.Cross {
			continue
		}
		crossOrigin++
		if s.Integrity == "" {
			sri = append(sri, s.Src)
		}
	}
	sriOK := "every cross-origin <script src> element carries an integrity hash"
	if crossOrigin == 0 {
		sriOK = "no cross-origin <script src> elements"
	}
	checks := []securityCheck{securityFinding(
		"sri",
		sriOK,
		"%d cross-origin <script src> element(s) load without an integrity attribute; add SRI hashes so a compromised host cannot inject code",
		sri,
	)}

	var forms []string
	for _, f := range probe.Forms {
		action := f.Action
		if action == "" {
			action = "(current URL)"
		}
		switch {
		case strings.HasPrefix(strings.ToLower(f.Action), "http://"):
			forms = append(forms, "http action: "+action)
		case f.Method == "get" && f.Password:
			forms = append(forms, "GET form with a password field: "+action)
		}
	}
	checks = append(checks, securityFinding(
		"form_target",
		"every form submits over https and no GET form carries a password field",
		"%d form(s) submit insecurely: an http action sends credentials in the clear, and a GET form with a password field puts the password in the URL",
		forms,
	))

	var weak []string
	for i, autocomplete := range probe.Passwords {
		value := strings.ToLower(strings.TrimSpace(autocomplete))
		if value == "current-password" || value == "new-password" {
			continue
		}
		weak = append(weak, fmt.Sprintf("password field %d: autocomplete=%q", i+1, autocomplete))
	}
	passOK := "every password field declares autocomplete=current-password or new-password"
	if len(probe.Passwords) == 0 {
		passOK = "no password fields"
	}
	checks = append(checks, securityFinding(
		"password_autocomplete",
		passOK,
		"%d password field(s) lack a current-password or new-password autocomplete hint; without it password managers guess the wrong credential",
		weak,
	))
	return checks
}

// securityCORSCheck flags the audited document's response when it pairs a
// wildcard origin with credentials, a combination browsers reject. headers is
// the merged document header map, which already prefers the page-side capture,
// so clear_logs cannot empty the evidence. Only the document response is judged;
// a subresource response is not part of that record.
func securityCORSCheck(headers map[string]string) securityCheck {
	origin := headerValue(headers, "access-control-allow-origin")
	credentials := headerValue(headers, "access-control-allow-credentials")
	if origin == "" {
		return securityCheck{
			ID:       "cors",
			Status:   "info",
			Evidence: "Access-Control-Allow-Origin absent from the document response",
			Detail:   "the document response declares no Access-Control-Allow-Origin, so there is no cross-origin grant to judge; subresource CORS responses are not audited",
		}
	}
	evidence := "Access-Control-Allow-Origin: " + origin
	if credentials == "" {
		evidence += "\nAccess-Control-Allow-Credentials absent"
	} else {
		evidence += "\nAccess-Control-Allow-Credentials: " + credentials
	}
	if origin != "*" {
		return securityCheck{
			ID:       "cors",
			Status:   "ok",
			Evidence: evidence,
			Detail:   "the document response scopes Access-Control-Allow-Origin to a single origin, so a wildcard cannot be paired with credentials",
		}
	}
	if strings.EqualFold(strings.TrimSpace(credentials), "true") {
		return securityCheck{
			ID:       "cors",
			Status:   "warn",
			Evidence: evidence,
			Detail:   "the document response sends Access-Control-Allow-Origin: * with Access-Control-Allow-Credentials: true; browsers reject the pair — echo the single allowed origin instead",
		}
	}
	return securityCheck{
		ID:       "cors",
		Status:   "ok",
		Evidence: evidence,
		Detail:   "the document response allows any origin but does not combine it with credentials",
	}
}

// securityFinding builds one aggregated check: ok when no value was flagged,
// warn otherwise, with evidence naming at most securityEvidenceLimit offenders.
func securityFinding(id, okDetail, warnDetail string, offenders []string) securityCheck {
	if len(offenders) == 0 {
		return securityCheck{ID: id, Status: "ok", Detail: okDetail}
	}
	evidence := strings.Join(offenders[:min(len(offenders), securityEvidenceLimit)], "\n")
	if len(offenders) > securityEvidenceLimit {
		evidence += fmt.Sprintf("\n(+%d more)", len(offenders)-securityEvidenceLimit)
	}
	return securityCheck{
		ID:       id,
		Status:   "warn",
		Evidence: evidence,
		Detail:   fmt.Sprintf(warnDetail, len(offenders)),
	}
}

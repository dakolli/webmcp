package pageops

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	"webmcp/pkg/browsers"
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
	// Checks are the findings in a stable order.
	Checks []securityCheck `json:"checks"`
	// Count is len(Checks).
	Count int `json:"count"`
	// Truncated counts DOM and cookie entries omitted by securityMaxItems, so a
	// cut is always explicit and never silent.
	Truncated int `json:"truncated"`
}

// PageSecurity returns a passive security audit of the live page as
// {"url","checks":[{id,status,evidence,detail}],"count","truncated"}. Every
// check reads truth the browser already holds — observed response headers from
// the page's network ring, the cookie jar for the current URL and one DOM
// evaluation — so the audit never reloads or probes the site.
func PageSecurity(ctx context.Context, p *browsers.Page) (string, error) {
	p.Lock()
	defer p.Unlock()
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

	entries, _ := p.NetworkEntries()
	headers, found, cut := securityDocumentHeaders(entries)
	cookies, cookiesOmitted := pageCookieChecks(pc, probe.URL, probe.Cookies)

	checks := securityHeaderChecks(probe, headers, found, cut)
	checks = append(checks, cookies...)
	checks = append(checks, securityMixedContentCheck(probe, entries))
	checks = append(checks, securityContentChecks(probe)...)
	checks = append(checks, securityCORSCheck(entries))

	report := securityReport{
		URL:       probe.URL,
		Checks:    checks,
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

// securityDocumentHeaders returns the newest observed document response's
// headers. found is false when the document request is no longer in the ring;
// cut reports a header set the engine bounded at capture.
func securityDocumentHeaders(entries []browsers.NetworkEntry) (headers map[string]string, found, cut bool) {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Type == string(proto.NetworkResourceTypeDocument) {
			return entries[i].RespHeaders, true, entries[i].RespHeadersTruncated
		}
	}
	return nil, false, false
}

// headerValue returns the value of name from a header map, matching the name
// case-insensitively because CDP preserves the wire casing.
func headerValue(headers map[string]string, name string) string {
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
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
			Detail: "the document response is no longer in the network ring (newest 200 requests), so every header below reads as absent — re-open the page to audit headers without guessing",
		})
	case cut:
		checks = append(checks, securityCheck{
			ID:     "headers",
			Status: "info",
			Detail: "the captured response headers were truncated at 32 headers / 4 KB, so a header reported absent below may have been dropped at capture",
		})
	}

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
		for _, weak := range []struct {
			id        string
			directive string
		}{
			{"csp_unsafe_inline", "'unsafe-inline'"},
			{"csp_unsafe_eval", "'unsafe-eval'"},
		} {
			check := securityCheck{
				ID:     weak.id,
				Status: "ok",
				Detail: "the policy does not allow " + weak.directive,
			}
			if strings.Contains(policy, weak.directive) {
				check.Status = "warn"
				check.Evidence = weak.directive
				check.Detail = "the policy allows " + weak.directive + " — use nonces or hashes for scripts and avoid eval-style code"
			}
			checks = append(checks, check)
		}
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

	xcto := strings.TrimSpace(headerValue(headers, "x-content-type-options"))
	if strings.EqualFold(xcto, "nosniff") {
		checks = append(checks, securityCheck{
			ID:       "x_content_type_options",
			Status:   "ok",
			Evidence: xcto,
			Detail:   "X-Content-Type-Options is nosniff",
		})
	} else {
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
// pageURL. readable is the document.cookie fallback used when the cookie API is
// unavailable; omitted counts cookies cut by securityMaxItems.
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

	checks = make([]securityCheck, 0, len(cookies))
	for _, c := range cookies {
		var missing []string
		if !c.HTTPOnly {
			missing = append(missing, "HttpOnly")
		}
		if !c.Secure {
			missing = append(missing, "Secure")
		}
		sameSite := string(c.SameSite)
		if c.SameSite != proto.NetworkCookieSameSiteStrict && c.SameSite != proto.NetworkCookieSameSiteLax {
			missing = append(missing, "SameSite")
		}
		check := securityCheck{
			ID:       "cookie:" + c.Name,
			Status:   "ok",
			Evidence: fmt.Sprintf("%s (domain=%s) HttpOnly=%t Secure=%t SameSite=%q", c.Name, c.Domain, c.HTTPOnly, c.Secure, sameSite),
			Detail:   "HttpOnly, Secure and SameSite are set",
		}
		if len(missing) > 0 {
			check.Status = "warn"
			check.Detail = "missing cookie flags: " + strings.Join(missing, ", ") +
				" — set HttpOnly so scripts cannot read the cookie, Secure so it is never sent over http, and SameSite to stop cross-site sends"
		}
		checks = append(checks, check)
	}
	return checks, omitted
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

// securityMixedContentCheck flags plain-http requests observed on an https page.
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
		"no http:// subresources were observed",
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

// securityCORSCheck flags an observed response that pairs a wildcard origin with
// credentials, a combination browsers reject.
func securityCORSCheck(entries []browsers.NetworkEntry) securityCheck {
	if len(entries) == 0 {
		return securityCheck{
			ID:     "cors",
			Status: "info",
			Detail: "no network responses have been observed yet",
		}
	}
	var offenders []string
	for _, e := range entries {
		if headerValue(e.RespHeaders, "access-control-allow-origin") != "*" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(headerValue(e.RespHeaders, "access-control-allow-credentials")), "true") {
			offenders = append(offenders, e.URL)
		}
	}
	return securityFinding(
		"cors",
		"no observed response allows any origin together with credentials",
		"%d response(s) send Access-Control-Allow-Origin: * with Access-Control-Allow-Credentials: true; browsers reject the pair — echo the one allowed origin instead",
		offenders,
	)
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

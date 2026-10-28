package browsers

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

const (
	// authMaxKeys caps each local/session storage key set in one snapshot.
	authMaxKeys = 500
	// authMaxDatabases caps how many IndexedDB databases are opened for names.
	authMaxDatabases = 20
)

// AuthCookie is one cookie in a named auth snapshot. Values are part of the
// stored payload and are never returned by a listing.
type AuthCookie struct {
	// Name is the cookie name.
	Name string `json:"name"`
	// Value is the cookie value.
	Value string `json:"value"`
	// Domain is the cookie domain as the browser reported it; a leading dot
	// marks a domain cookie and is preserved on restore.
	Domain string `json:"domain"`
	// Path is the cookie path.
	Path string `json:"path"`
	// Secure reports the Secure flag.
	Secure bool `json:"secure"`
	// HTTPOnly reports the HttpOnly flag.
	HTTPOnly bool `json:"http_only"`
	// SameSite is "Strict", "Lax", "None", or empty when unspecified.
	SameSite string `json:"same_site"`
	// Expires is the expiry in epoch seconds, -1 for a session cookie.
	Expires int64 `json:"expires"`
}

// Cookie is one cookie in the browser's cookie jar with its scope fields
// intact. Values are raw, so a caller that exports them applies its own
// redaction rule.
type Cookie struct {
	// Name is the cookie name.
	Name string
	// Value is the cookie value.
	Value string
	// Domain is the cookie domain as the browser reported it.
	Domain string
	// Path is the cookie path.
	Path string
	// Secure reports the Secure flag.
	Secure bool
	// HTTPOnly reports the HttpOnly flag.
	HTTPOnly bool
	// Expires is the expiry in epoch seconds, -1 for a session cookie.
	Expires int64
}

// AuthDatabase is one IndexedDB database in a snapshot: its name and object
// store names only. Values are deliberately not serialized.
type AuthDatabase struct {
	// Name is the database name.
	Name string `json:"name"`
	// Stores are the object store names.
	Stores []string `json:"stores"`
}

// AuthOrigin is the storage of one origin in a snapshot.
type AuthOrigin struct {
	// Origin is the scheme://host origin.
	Origin string `json:"origin"`
	// LocalStorage holds the origin's localStorage key/value pairs.
	LocalStorage map[string]string `json:"local_storage"`
	// SessionStorage holds the origin's sessionStorage key/value pairs.
	SessionStorage map[string]string `json:"session_storage"`
	// IndexedDB lists database and store names; contents are never captured.
	IndexedDB []AuthDatabase `json:"indexed_db,omitempty"`
}

// AuthSnapshot is the portable auth state of one site: cookies for that host
// set plus per-origin storage and the user agent it was captured with.
type AuthSnapshot struct {
	// Cookies are the cookies whose domain matches the site.
	Cookies []AuthCookie `json:"cookies"`
	// Origins are the captured origins (at most one per page today).
	Origins []AuthOrigin `json:"origins"`
	// UserAgent is navigator.userAgent at capture time.
	UserAgent string `json:"user_agent"`
	// SavedAt is the capture time.
	SavedAt time.Time `json:"saved_at"`
}

// AuthRestoreReport is what one restore changed.
type AuthRestoreReport struct {
	// Cookies is the number of cookies written.
	Cookies int `json:"cookies"`
	// Origins is the number of origins whose storage was written.
	Origins int `json:"origins"`
	// LocalStorage is the number of localStorage keys written.
	LocalStorage int `json:"local_storage"`
	// SessionStorage is the number of sessionStorage keys written.
	SessionStorage int `json:"session_storage"`
	// Warnings names every skipped item or forced mismatch.
	Warnings []string `json:"warnings,omitempty"`
}

// authStorageProbeJS reads the page origin's storage in one evaluation. Key
// counts are capped in the page so a hostile origin cannot make the payload
// unbounded; IndexedDB contributes database and store names only.
const authStorageProbeJS = `async (maxKeys, maxDatabases) => {
  const read = (store) => {
    const out = {};
    try {
      const n = Math.min(store.length, maxKeys);
      for (let i = 0; i < n; i++) {
        const key = store.key(i);
        if (key !== null) out[key] = store.getItem(key) || '';
      }
    } catch (e) {}
    return out;
  };
  const databases = [];
  try {
    const names = (await indexedDB.databases()).map((d) => d.name).filter((n) => !!n);
    for (const name of names.slice(0, maxDatabases)) {
      const stores = await new Promise((resolve) => {
        const req = indexedDB.open(name);
        req.onsuccess = () => {
          const db = req.result;
          const found = [...db.objectStoreNames];
          db.close();
          resolve(found);
        };
        req.onerror = () => resolve([]);
        req.onblocked = () => resolve([]);
      });
      databases.push({ name: name, stores: stores });
    }
  } catch (e) {}
  return {
    origin: location.origin,
    local: read(localStorage),
    session: read(sessionStorage),
    indexed_db: databases,
  };
}`

// authStorageWriteJS writes the snapshot's localStorage and sessionStorage
// pairs into the live origin. It never navigates.
const authStorageWriteJS = `(local, session) => {
  const write = (store, pairs) => {
    let n = 0;
    for (const key of Object.keys(pairs || {})) {
      try { store.setItem(key, pairs[key]); n++; } catch (e) {}
    }
    return n;
  };
  return { local: write(localStorage, local), session: write(sessionStorage, session) };
}`

// authProbe is the payload authStorageProbeJS returns.
type authProbe struct {
	Origin    string            `json:"origin"`
	Local     map[string]string `json:"local"`
	Session   map[string]string `json:"session"`
	IndexedDB []AuthDatabase    `json:"indexed_db"`
}

// authWriteResult is the payload authStorageWriteJS returns.
type authWriteResult struct {
	Local   int `json:"local"`
	Session int `json:"session"`
}

// NormalizeAuthSite reduces a site argument to its bare lowercase host. It
// accepts a host or an absolute URL and strips the scheme, path and port.
func NormalizeAuthSite(site string) (string, error) {
	raw := strings.TrimSpace(site)
	if raw == "" {
		return "", fmt.Errorf("auth: empty site — pass a host such as example.org")
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("auth: invalid site %q: %w", site, err)
		}
		raw = u.Host
	}
	if i := strings.IndexByte(raw, '/'); i >= 0 {
		raw = raw[:i]
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	raw = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "."))
	if raw == "" {
		return "", fmt.Errorf("auth: invalid site %q — pass a host such as example.org", site)
	}
	return raw, nil
}

// cookieMatchesSite reports whether a cookie domain belongs to site: an exact
// host match or a subdomain suffix. The leading dot is ignored for matching
// only; it is preserved on the cookie itself.
func cookieMatchesSite(domain, site string) bool {
	d := strings.ToLower(strings.TrimPrefix(domain, "."))
	if d == "" || site == "" {
		return false
	}
	return d == site || strings.HasSuffix(d, "."+site)
}

// originOf reduces a URL to scheme://host, or "" when it has no origin.
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// CaptureAuth reads the cookies for site and the current origin's storage from
// the page. It takes the page lock itself; callers must not pre-lock.
func (p *Page) CaptureAuth(ctx context.Context, site string) (*AuthSnapshot, error) {
	normalized, err := NormalizeAuthSite(site)
	if err != nil {
		return nil, err
	}
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return nil, fmt.Errorf("page session is closed")
	}

	pc := p.Rod.Context(ctx)
	info, err := pc.Info()
	if err != nil {
		return nil, fmt.Errorf("read page url: %w", err)
	}
	ua, err := pc.Eval(`() => navigator.userAgent`)
	if err != nil {
		return nil, fmt.Errorf("read user agent: %w", err)
	}
	cookies, err := authCookies(pc, normalized)
	if err != nil {
		return nil, err
	}
	origins, err := authOrigins(pc, info.URL)
	if err != nil {
		return nil, err
	}
	return &AuthSnapshot{
		Cookies:   cookies,
		Origins:   origins,
		UserAgent: ua.Value.Str(),
		SavedAt:   time.Now().UTC(),
	}, nil
}

// ApplyAuth writes the snapshot's cookies and storage into the page's browser
// context. A user-agent mismatch refuses the restore unless force is true. It
// takes the page lock itself; callers must not pre-lock.
func (p *Page) ApplyAuth(
	ctx context.Context,
	snap *AuthSnapshot,
	force bool,
) (AuthRestoreReport, error) {
	var report AuthRestoreReport
	if snap == nil {
		return report, fmt.Errorf("empty snapshot")
	}
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return report, fmt.Errorf("page session is closed")
	}

	pc := p.Rod.Context(ctx)
	ua, err := pc.Eval(`() => navigator.userAgent`)
	if err != nil {
		return report, fmt.Errorf("read user agent: %w", err)
	}
	liveUA := ua.Value.Str()
	if snap.UserAgent != "" && liveUA != snap.UserAgent {
		if !force {
			return report, fmt.Errorf(
				"stored user agent %q does not match the live page user agent %q — re-capture on this browser, or pass force=true to restore anyway",
				snap.UserAgent,
				liveUA,
			)
		}
		report.Warnings = append(report.Warnings, fmt.Sprintf(
			"forced user-agent mismatch: stored %q, live %q",
			snap.UserAgent,
			liveUA,
		))
	}

	report.Cookies, err = applyAuthCookies(pc, snap.Cookies, &report)
	if err != nil {
		return report, err
	}
	applyAuthOrigins(pc, snap.Origins, &report)
	return report, nil
}

// authCookies reads the browser-context cookies and keeps the ones whose domain
// matches site, sorted deterministically.
func authCookies(pc *rod.Page, site string) ([]AuthCookie, error) {
	b := pc.Browser()
	if b == nil {
		return nil, fmt.Errorf("no browser for the page")
	}
	res, err := (proto.StorageGetCookies{BrowserContextID: b.BrowserContextID}).Call(b)
	if err != nil {
		return nil, fmt.Errorf("read cookies: %w", err)
	}
	out := make([]AuthCookie, 0, len(res.Cookies))
	for _, c := range res.Cookies {
		if !cookieMatchesSite(c.Domain, site) {
			continue
		}
		out = append(out, AuthCookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Secure:   c.Secure,
			HTTPOnly: c.HTTPOnly,
			SameSite: string(c.SameSite),
			Expires:  int64(c.Expires),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Domain != out[j].Domain {
			return out[i].Domain < out[j].Domain
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Cookies snapshots the browser context's cookie jar without filtering it to a
// site; matching a cookie to a request belongs to the consumer. It takes the
// page lock itself; callers must not pre-lock.
func (p *Page) Cookies(ctx context.Context) ([]Cookie, error) {
	if err := p.lockRead(ctx, "cookies"); err != nil {
		return nil, err
	}
	defer p.Unlock()
	if p.Rod == nil {
		return nil, fmt.Errorf("cookies: page session is closed")
	}
	b := p.Rod.Browser()
	if b == nil {
		return nil, fmt.Errorf("cookies: no browser for the page")
	}
	res, err := (proto.StorageGetCookies{
		BrowserContextID: b.BrowserContextID,
	}).Call(b.Context(ctx))
	if err != nil {
		return nil, fmt.Errorf("cookies: read browser cookie jar: %w", err)
	}
	out := make([]Cookie, 0, len(res.Cookies))
	for _, c := range res.Cookies {
		out = append(out, Cookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Secure:   c.Secure,
			HTTPOnly: c.HTTPOnly,
			Expires:  int64(c.Expires),
		})
	}
	return out, nil
}

// authOrigins reads the current origin's local and session storage plus the
// IndexedDB database/store names.
func authOrigins(pc *rod.Page, pageURL string) ([]AuthOrigin, error) {
	origin := originOf(pageURL)
	if origin == "" {
		return nil, nil
	}
	res, err := pc.Eval(authStorageProbeJS, authMaxKeys, authMaxDatabases)
	if err != nil {
		return nil, fmt.Errorf("read storage: %w", err)
	}
	var probe authProbe
	if err := res.Value.Unmarshal(&probe); err != nil {
		return nil, fmt.Errorf("decode storage: %w", err)
	}
	if probe.Origin != "" {
		origin = probe.Origin
	}
	if probe.Local == nil {
		probe.Local = map[string]string{}
	}
	if probe.Session == nil {
		probe.Session = map[string]string{}
	}
	return []AuthOrigin{{
		Origin:         origin,
		LocalStorage:   probe.Local,
		SessionStorage: probe.Session,
		IndexedDB:      probe.IndexedDB,
	}}, nil
}

// applyAuthCookies converts and writes the snapshot cookies into the page's
// browser context. A cookie that cannot be mapped is skipped with a warning.
func applyAuthCookies(
	pc *rod.Page,
	cookies []AuthCookie,
	report *AuthRestoreReport,
) (int, error) {
	params := make([]*proto.NetworkCookieParam, 0, len(cookies))
	for _, c := range cookies {
		param, err := cookieParam(c)
		if err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("cookie %q skipped: %v", c.Name, err))
			continue
		}
		params = append(params, param)
	}
	if len(params) == 0 {
		return 0, nil
	}
	b := pc.Browser()
	if b == nil {
		return 0, fmt.Errorf("no browser for the page")
	}
	err := (proto.StorageSetCookies{
		Cookies:          params,
		BrowserContextID: b.BrowserContextID,
	}).Call(b)
	if err != nil {
		return 0, fmt.Errorf("write cookies: %w", err)
	}
	return len(params), nil
}

// cookieParam maps one snapshot cookie to a CDP cookie parameter without
// broadening its scope: a domain cookie keeps its leading dot, and a host-only
// cookie is written through a URL with Domain omitted.
func cookieParam(c AuthCookie) (*proto.NetworkCookieParam, error) {
	domain := strings.TrimSpace(c.Domain)
	if c.Name == "" {
		return nil, fmt.Errorf("empty cookie name")
	}
	if domain == "" {
		return nil, fmt.Errorf("empty cookie domain")
	}
	param := &proto.NetworkCookieParam{
		Name:     c.Name,
		Value:    c.Value,
		Path:     c.Path,
		Secure:   c.Secure,
		HTTPOnly: c.HTTPOnly,
		SameSite: sameSite(c.SameSite),
	}
	if c.Expires > 0 {
		param.Expires = proto.TimeSinceEpoch(c.Expires)
	}
	if strings.HasPrefix(domain, ".") {
		param.Domain = domain
		param.URL = cookieURL(domain, c.Path, c.Secure)
		return param, nil
	}
	param.URL = cookieURL(domain, c.Path, c.Secure)
	return param, nil
}

// cookieURL builds the request URL a host-only cookie is set through.
func cookieURL(domain, path string, secure bool) string {
	scheme := "http"
	if secure {
		scheme = "https"
	}
	host := strings.TrimPrefix(domain, ".")
	if path == "" {
		path = "/"
	}
	return scheme + "://" + host + path
}

// sameSite validates the snapshot's SameSite value against the CDP vocabulary,
// dropping an unknown value rather than failing the whole cookie write.
func sameSite(value string) proto.NetworkCookieSameSite {
	switch proto.NetworkCookieSameSite(value) {
	case proto.NetworkCookieSameSiteStrict,
		proto.NetworkCookieSameSiteLax,
		proto.NetworkCookieSameSiteNone:
		return proto.NetworkCookieSameSite(value)
	default:
		return ""
	}
}

// applyAuthOrigins writes storage for origins the live page is currently on.
// A non-matching origin is skipped with a warning because writing it would
// require an unexpected navigation.
func applyAuthOrigins(
	pc *rod.Page,
	origins []AuthOrigin,
	report *AuthRestoreReport,
) {
	info, err := pc.Info()
	if err != nil {
		report.Warnings = append(report.Warnings, fmt.Sprintf("read live origin: %v", err))
		return
	}
	live := originOf(info.URL)
	for _, origin := range origins {
		if live == "" || origin.Origin != live {
			report.Warnings = append(report.Warnings, fmt.Sprintf(
				"origin %q skipped: restore it on a page at that origin (live page is %q)",
				origin.Origin,
				live,
			))
			continue
		}
		res, err := pc.Eval(authStorageWriteJS, origin.LocalStorage, origin.SessionStorage)
		if err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf(
				"origin %q storage skipped: %v",
				origin.Origin,
				err,
			))
			continue
		}
		var written authWriteResult
		if err := res.Value.Unmarshal(&written); err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf(
				"origin %q storage result unreadable: %v",
				origin.Origin,
				err,
			))
			continue
		}
		report.Origins++
		report.LocalStorage += written.Local
		report.SessionStorage += written.Session
	}
}

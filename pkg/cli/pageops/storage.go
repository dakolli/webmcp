package pageops

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	"webmcp/pkg/browsers"
)

const (
	// storageMaxCookies bounds the cookie list; the remainder is reported in
	// cookies_truncated.
	storageMaxCookies = 200
	// storageMaxKeys bounds each localStorage/sessionStorage key list.
	storageMaxKeys = 200
	// storageMaxDatabases bounds how many IndexedDB databases are opened.
	storageMaxDatabases = 20
	// storageMaxCaches bounds the Cache Storage name list.
	storageMaxCaches = 40
	// redactedValue replaces every cookie and storage value unless the caller
	// asked for values.
	redactedValue = "<redacted>"
)

// storageProbeJS reads every storage surface the page can reach in one
// evaluation. Each access is guarded: localStorage throws on a sandboxed or
// cookie-blocked page, `caches` is absent outside a secure context, and an
// IndexedDB open can be blocked by another tab. The caps are applied in the page
// so a page with thousands of keys cannot make the probe payload unbounded;
// each section also reports its true total for the truncation counters.
const storageProbeJS = `async (includeValues, maxKeys, maxDatabases, maxCaches) => {
  const entries = (store) => {
    const total = store.length;
    const items = [];
    for (let i = 0; i < Math.min(total, maxKeys); i++) {
      const key = store.key(i);
      if (key === null) continue;
      const value = store.getItem(key) || '';
      items.push({ key: key, value_length: value.length, value: includeValues ? value : '' });
    }
    return { items: items, total: total };
  };
  let local = { items: [], total: 0 };
  try { local = entries(localStorage); } catch (e) {}
  let session = { items: [], total: 0 };
  try { session = entries(sessionStorage); } catch (e) {}
  const databases = { items: [], total: 0 };
  try {
    const names = (await indexedDB.databases()).map((info) => info.name).filter((name) => !!name);
    databases.total = names.length;
    for (const name of names.slice(0, maxDatabases)) {
      const storeNames = await new Promise((resolve) => {
        const req = indexedDB.open(name);
        req.onsuccess = () => {
          const db = req.result;
          const stores = [...db.objectStoreNames];
          db.close();
          resolve(stores);
        };
        req.onerror = () => resolve([]);
        req.onblocked = () => resolve([]);
      });
      databases.items.push({ name: name, stores: storeNames });
    }
  } catch (e) {}
  const cachesSection = { items: [], total: 0 };
  try {
    const names = await caches.keys();
    cachesSection.total = names.length;
    cachesSection.items = names.slice(0, maxCaches);
  } catch (e) {}
  return {
    local_storage: local,
    session_storage: session,
    indexed_db: databases,
    caches: cachesSection,
  };
}`

// StorageCookie is one cookie with its security flags. Value is redactedValue
// unless the caller asked for values; Expires is epoch seconds, -1 for a
// session cookie.
type StorageCookie struct {
	// Name is the cookie name.
	Name string `json:"name"`
	// Value is the cookie value, or redactedValue.
	Value string `json:"value"`
	// Domain is the cookie domain.
	Domain string `json:"domain"`
	// Path is the cookie path.
	Path string `json:"path"`
	// Secure reports the Secure flag.
	Secure bool `json:"secure"`
	// HTTPOnly reports the HttpOnly flag.
	HTTPOnly bool `json:"http_only"`
	// SameSite is "Strict", "Lax", "None", or empty when unspecified.
	SameSite string `json:"same_site"`
	// Expires is the expiry in epoch seconds, -1 when the cookie is a session
	// cookie.
	Expires int64 `json:"expires"`
	// Size is the cookie size in bytes as reported by the browser.
	Size int `json:"size"`
}

// StorageItem is one localStorage or sessionStorage entry: its key, the value's
// character length and the value itself (redactedValue unless values were
// requested).
type StorageItem struct {
	// Key is the storage key.
	Key string `json:"key"`
	// ValueLength is the value's character length, reported even when the value
	// is redacted.
	ValueLength int `json:"value_length"`
	// Value is the stored value, or redactedValue.
	Value string `json:"value"`
}

// StorageDatabase is one IndexedDB database and the names of its object stores.
type StorageDatabase struct {
	// Name is the database name.
	Name string `json:"name"`
	// Stores are the object store names, empty when the database could not be
	// opened.
	Stores []string `json:"stores"`
}

// StorageReport is the JSON document PageStorage returns. Every section is
// always present (as an empty array when the page has no such storage) and each
// *_truncated counter reports how many entries its cap left out.
type StorageReport struct {
	// Cookies are the browser-context cookies, sorted by name then domain.
	Cookies []StorageCookie `json:"cookies"`
	// CookiesTruncated counts cookies omitted by storageMaxCookies.
	CookiesTruncated int `json:"cookies_truncated"`
	// LocalStorage is the localStorage key list.
	LocalStorage []StorageItem `json:"local_storage"`
	// LocalStorageTruncated counts keys omitted by storageMaxKeys.
	LocalStorageTruncated int `json:"local_storage_truncated"`
	// SessionStorage is the sessionStorage key list.
	SessionStorage []StorageItem `json:"session_storage"`
	// SessionStorageTruncated counts keys omitted by storageMaxKeys.
	SessionStorageTruncated int `json:"session_storage_truncated"`
	// IndexedDB is the database list with each database's object store names.
	IndexedDB []StorageDatabase `json:"indexed_db"`
	// IndexedDBTruncated counts databases omitted by storageMaxDatabases.
	IndexedDBTruncated int `json:"indexed_db_truncated"`
	// Caches is the Cache Storage name list.
	Caches []string `json:"caches"`
	// CachesTruncated counts caches omitted by storageMaxCaches.
	CachesTruncated int `json:"caches_truncated"`
}

// storageValues is one localStorage/sessionStorage section as the probe returns
// it: the capped items plus the store's true length.
type storageValues struct {
	// Items are the capped key entries.
	Items []StorageItem `json:"items"`
	// Total is the store's true key count.
	Total int `json:"total"`
}

// storageDatabases is the IndexedDB section as the probe returns it.
type storageDatabases struct {
	// Items are the capped databases.
	Items []StorageDatabase `json:"items"`
	// Total is the true database count.
	Total int `json:"total"`
}

// storageNames is the Cache Storage section as the probe returns it.
type storageNames struct {
	// Items are the capped cache names.
	Items []string `json:"items"`
	// Total is the true cache count.
	Total int `json:"total"`
}

// storageProbe is the payload one in-page evaluation returns, one field per
// section.
type storageProbe struct {
	LocalStorage   storageValues    `json:"local_storage"`
	SessionStorage storageValues    `json:"session_storage"`
	IndexedDB      storageDatabases `json:"indexed_db"`
	Caches         storageNames     `json:"caches"`
}

// PageStorage returns a read-only snapshot of the page's browser context:
// cookies with their security flags plus localStorage, sessionStorage, IndexedDB
// database/store names and Cache Storage names, as one JSON object. Every value
// is the literal <redacted> unless includeValues is true, which exposes live
// session tokens. Cookie writes stay out of scope; this never calls SetCookies.
func PageStorage(
	ctx context.Context,
	p *browsers.Page,
	includeValues bool,
) (string, error) {
	ctx, done, err := beginPage(ctx, p, "storage")
	if err != nil {
		return "", err
	}
	defer done()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	pc := p.Rod.Context(ctx)
	res, err := pc.Eval(
		storageProbeJS,
		includeValues,
		storageMaxKeys,
		storageMaxDatabases,
		storageMaxCaches,
	)
	if err != nil {
		return "", fmt.Errorf("evaluate storage probe: %w", err)
	}
	var probe storageProbe
	if err := res.Value.Unmarshal(&probe); err != nil {
		return "", fmt.Errorf("decode storage probe: %w", err)
	}

	cookies, err := storageCookies(pc)
	if err != nil {
		return "", err
	}

	b, err := json.Marshal(buildStorageReport(probe, cookies, includeValues))
	if err != nil {
		return "", fmt.Errorf("encode storage report: %w", err)
	}
	return string(b), nil
}

// storageCookies reads every cookie in the page's browser context, falling back
// to the cookies the browser would send to the current URL when the browser-wide
// read is unavailable.
func storageCookies(pc *rod.Page) ([]*proto.NetworkCookie, error) {
	if b := pc.Browser(); b != nil {
		if res, err := (proto.StorageGetCookies{BrowserContextID: b.BrowserContextID}).Call(b); err == nil {
			return res.Cookies, nil
		}
	}
	cookies, err := pc.Cookies([]string{})
	if err != nil {
		return nil, fmt.Errorf("read cookies: %w", err)
	}
	return cookies, nil
}

// buildStorageReport shapes the probe and the cookie jar into the wire document:
// it sorts and caps the cookies, applies the per-section caps, redacts values
// unless includeValues is set, and reports the omitted count per section.
func buildStorageReport(
	probe storageProbe,
	cookies []*proto.NetworkCookie,
	includeValues bool,
) StorageReport {
	sort.Slice(cookies, func(i, j int) bool {
		if cookies[i].Name != cookies[j].Name {
			return cookies[i].Name < cookies[j].Name
		}
		return cookies[i].Domain < cookies[j].Domain
	})
	cookieItems, cookiesCut := capStorage(cookies, len(cookies), storageMaxCookies)

	report := StorageReport{
		Cookies:          make([]StorageCookie, 0, len(cookieItems)),
		CookiesTruncated: cookiesCut,
	}
	for _, c := range cookieItems {
		report.Cookies = append(report.Cookies, StorageCookie{
			Name:     c.Name,
			Value:    storageValue(c.Value, includeValues),
			Domain:   c.Domain,
			Path:     c.Path,
			Secure:   c.Secure,
			HTTPOnly: c.HTTPOnly,
			SameSite: string(c.SameSite),
			Expires:  int64(c.Expires),
			Size:     c.Size,
		})
	}

	report.LocalStorage, report.LocalStorageTruncated = storageItems(probe.LocalStorage, includeValues)
	report.SessionStorage, report.SessionStorageTruncated = storageItems(probe.SessionStorage, includeValues)
	report.IndexedDB, report.IndexedDBTruncated = storageDatabasesOf(probe.IndexedDB)
	report.Caches, report.CachesTruncated = storageCacheNames(probe.Caches)
	return report
}

// storageItems caps and redacts one key/value section.
func storageItems(section storageValues, includeValues bool) ([]StorageItem, int) {
	items, cut := capStorage(section.Items, section.Total, storageMaxKeys)
	out := make([]StorageItem, 0, len(items))
	for _, item := range items {
		out = append(out, StorageItem{
			Key:         item.Key,
			ValueLength: item.ValueLength,
			Value:       storageValue(item.Value, includeValues),
		})
	}
	return out, cut
}

// storageDatabasesOf caps the database list and normalizes every store list to a
// non-nil slice so the JSON never carries a null for an empty database.
func storageDatabasesOf(section storageDatabases) ([]StorageDatabase, int) {
	items, cut := capStorage(section.Items, section.Total, storageMaxDatabases)
	out := make([]StorageDatabase, 0, len(items))
	for _, db := range items {
		stores := db.Stores
		if stores == nil {
			stores = []string{}
		}
		out = append(out, StorageDatabase{Name: db.Name, Stores: stores})
	}
	return out, cut
}

// storageCacheNames caps the Cache Storage name list.
func storageCacheNames(section storageNames) ([]string, int) {
	items, cut := capStorage(section.Items, section.Total, storageMaxCaches)
	out := make([]string, len(items))
	copy(out, items)
	return out, cut
}

// storageValue returns the value itself when values were requested, otherwise
// the redaction literal.
func storageValue(value string, includeValues bool) string {
	if includeValues {
		return value
	}
	return redactedValue
}

// capStorage truncates items to limit and returns how many the cap omitted,
// treating a total smaller than the received items as the items' own length so a
// malformed probe can never produce a negative count.
func capStorage[T any](items []T, total, limit int) ([]T, int) {
	if len(items) > limit {
		items = items[:limit]
	}
	if total < len(items) {
		total = len(items)
	}
	return items, total - len(items)
}

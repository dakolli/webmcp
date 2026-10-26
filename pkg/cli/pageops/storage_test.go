package pageops

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/go-rod/rod/lib/proto"
)

// TestBuildStorageReport pins the storage envelope: every section is present
// even when empty, values are redacted unless requested, and each section
// reports how many entries its cap omitted.
func TestBuildStorageReport(t *testing.T) {
	t.Run("all sections present when empty", func(t *testing.T) {
		b, err := json.Marshal(buildStorageReport(storageProbe{}, nil, false))
		if err != nil {
			t.Fatalf("marshal report: %v", err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(b, &fields); err != nil {
			t.Fatalf("decode report: %v", err)
		}
		for _, key := range []string{
			"cookies",
			"cookies_truncated",
			"local_storage",
			"local_storage_truncated",
			"session_storage",
			"session_storage_truncated",
			"indexed_db",
			"indexed_db_truncated",
			"caches",
			"caches_truncated",
		} {
			v, ok := fields[key]
			if !ok {
				t.Errorf("report is missing %q", key)
				continue
			}
			if key == "cookies" || key == "local_storage" || key == "session_storage" ||
				key == "indexed_db" || key == "caches" {
				if string(v) != "[]" {
					t.Errorf("%s = %s, want []", key, v)
				}
			}
		}
	})

	t.Run("redacts by default and restores on request", func(t *testing.T) {
		probe := storageProbe{
			LocalStorage: storageValues{
				Items: []StorageItem{{Key: "token", ValueLength: 32, Value: "live-token"}},
				Total: 1,
			},
			SessionStorage: storageValues{
				Items: []StorageItem{{Key: "csrf", ValueLength: 4, Value: "abcd"}},
				Total: 1,
			},
		}
		cookies := []*proto.NetworkCookie{{
			Name:     "sid",
			Value:    "secret",
			Domain:   "example.com",
			Path:     "/",
			Secure:   true,
			HTTPOnly: true,
			SameSite: proto.NetworkCookieSameSiteLax,
			Expires:  -1,
			Size:     12,
		}}

		redacted := buildStorageReport(probe, cookies, false)
		if got := redacted.Cookies[0].Value; got != redactedValue {
			t.Errorf("cookie value = %q, want %q", got, redactedValue)
		}
		if got := redacted.LocalStorage[0].Value; got != redactedValue {
			t.Errorf("localStorage value = %q, want %q", got, redactedValue)
		}
		if got := redacted.LocalStorage[0].ValueLength; got != 32 {
			t.Errorf("localStorage value_length = %d, want 32", got)
		}
		if got := redacted.Cookies[0]; !got.Secure || !got.HTTPOnly || got.SameSite != "Lax" {
			t.Errorf("cookie flags not reported: %+v", got)
		}

		open := buildStorageReport(probe, cookies, true)
		if got := open.Cookies[0].Value; got != "secret" {
			t.Errorf("cookie value = %q, want secret", got)
		}
		if got := open.LocalStorage[0].Value; got != "live-token" {
			t.Errorf("localStorage value = %q, want live-token", got)
		}
		if got := open.SessionStorage[0].Value; got != "abcd" {
			t.Errorf("sessionStorage value = %q, want abcd", got)
		}
	})

	t.Run("caps each section and reports omitted", func(t *testing.T) {
		cookies := make([]*proto.NetworkCookie, storageMaxCookies+2)
		for i := range cookies {
			cookies[i] = &proto.NetworkCookie{Name: fmt.Sprintf("c%03d", i)}
		}
		keys := make([]StorageItem, storageMaxKeys+3)
		for i := range keys {
			keys[i] = StorageItem{Key: fmt.Sprintf("k%03d", i)}
		}
		databases := make([]StorageDatabase, storageMaxDatabases+1)
		for i := range databases {
			databases[i] = StorageDatabase{Name: fmt.Sprintf("db%d", i)}
		}
		cacheNames := make([]string, storageMaxCaches+4)
		for i := range cacheNames {
			cacheNames[i] = fmt.Sprintf("cache-%d", i)
		}

		report := buildStorageReport(storageProbe{
			LocalStorage:   storageValues{Items: keys, Total: len(keys)},
			SessionStorage: storageValues{Items: keys, Total: len(keys)},
			IndexedDB:      storageDatabases{Items: databases, Total: len(databases)},
			Caches:         storageNames{Items: cacheNames, Total: len(cacheNames)},
		}, cookies, false)

		checks := []struct {
			name      string
			got, want int
			omitted   *int
			wantCut   int
		}{
			{"cookies", len(report.Cookies), storageMaxCookies, &report.CookiesTruncated, 2},
			{"local_storage", len(report.LocalStorage), storageMaxKeys, &report.LocalStorageTruncated, 3},
			{"session_storage", len(report.SessionStorage), storageMaxKeys, &report.SessionStorageTruncated, 3},
			{"indexed_db", len(report.IndexedDB), storageMaxDatabases, &report.IndexedDBTruncated, 1},
			{"caches", len(report.Caches), storageMaxCaches, &report.CachesTruncated, 4},
		}
		for _, c := range checks {
			if c.got != c.want {
				t.Errorf("%s kept %d entries, want %d", c.name, c.got, c.want)
			}
			if *c.omitted != c.wantCut {
				t.Errorf("%s truncated = %d, want %d", c.name, *c.omitted, c.wantCut)
			}
		}
		if got := report.Cookies[0].Name; got != "c000" {
			t.Errorf("cookie order changed: first = %q, want c000", got)
		}
	})
}

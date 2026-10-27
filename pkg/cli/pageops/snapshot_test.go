package pageops

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dakolli/webmcp/pkg/store"
)

// useTempAuthDB points the snapshot helpers at a throwaway state database for
// the duration of one test.
func useTempAuthDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	previous := authStateDBPath
	authStateDBPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { authStateDBPath = previous })
	return path
}

// TestAuthListRedactsValues is the redaction proof: a saved cookie value, a
// localStorage value and a sessionStorage value must never appear in the
// listing JSON.
func TestAuthListRedactsValues(t *testing.T) {
	ctx := context.Background()
	path := useTempAuthDB(t)
	s, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	payload := `{"cookies":[{"name":"sid","value":"super-secret-cookie"}],` +
		`"origins":[{"origin":"https://example.org",` +
		`"local_storage":{"token":"super-secret-storage"},` +
		`"session_storage":{"session":"super-secret-session"}}],` +
		`"user_agent":"TestAgent/1"}`
	if _, err := s.SaveAuthSnapshot(ctx, "example.org", "main", []byte(payload)); err != nil {
		t.Fatalf("SaveAuthSnapshot: %v", err)
	}
	if _, err := s.SaveAuthSnapshot(ctx, "other.example", "alt", []byte(payload)); err != nil {
		t.Fatalf("second SaveAuthSnapshot: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	out, err := PageAuthList(ctx, "")
	if err != nil {
		t.Fatalf("PageAuthList: %v", err)
	}
	for _, secret := range []string{"super-secret-cookie", "super-secret-storage", "super-secret-session"} {
		if strings.Contains(out, secret) {
			t.Fatalf("listing leaked %q: %s", secret, out)
		}
	}

	var listed authListResult
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	if listed.Count != 2 || len(listed.Snapshots) != 2 {
		t.Fatalf("listing count = %d/%d, want 2", listed.Count, len(listed.Snapshots))
	}
	if listed.Snapshots[0].CookieCount != 1 || listed.Snapshots[0].OriginCount != 1 {
		t.Fatalf("summary = %+v, want 1 cookie and 1 origin", listed.Snapshots[0])
	}
	if !strings.Contains(listed.Warning, "unencrypted") {
		t.Errorf("warning = %q, want the at-rest warning", listed.Warning)
	}

	filtered, err := PageAuthList(ctx, "https://example.org/ignored")
	if err != nil {
		t.Fatalf("PageAuthList(site): %v", err)
	}
	var one authListResult
	if err := json.Unmarshal([]byte(filtered), &one); err != nil {
		t.Fatalf("decode filtered listing: %v", err)
	}
	if one.Count != 1 || one.Snapshots[0].Site != "example.org" {
		t.Fatalf("filtered listing = %+v, want only example.org", one.Snapshots)
	}
}

func TestAuthTargetValidation(t *testing.T) {
	if _, _, err := authTarget("", "main"); err == nil {
		t.Error("empty site was accepted")
	}
	if _, _, err := authTarget("example.org", ""); err == nil {
		t.Error("empty name was accepted")
	}
	site, name, err := authTarget("https://Example.ORG/path", "main")
	if err != nil {
		t.Fatalf("authTarget: %v", err)
	}
	if site != "example.org" || name != "main" {
		t.Errorf("authTarget = %q/%q, want example.org/main", site, name)
	}
}

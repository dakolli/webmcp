package pageops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dakolli/webmcp/pkg/browsers"
	"github.com/dakolli/webmcp/pkg/config"
	"github.com/dakolli/webmcp/pkg/store"
)

// authStateDBPath resolves the state database the auth snapshots live in. It is
// a variable so tests can point the snapshot helpers at a temporary database
// without touching the user's config directory.
var authStateDBPath = config.StateDBPath

// authStoreWarning is carried by every auth result: the store is local,
// plaintext and 0600, so the snapshot file itself is a credential.
const authStoreWarning = "auth snapshots are stored unencrypted in the local state database (mode 0600) — treat that file as a credential; no result, error or log line prints a cookie or storage value"

// authSaveResult is the redacted outcome of one auth_save.
type authSaveResult struct {
	Site      string `json:"site"`
	Name      string `json:"name"`
	Cookies   int    `json:"cookies"`
	Origins   int    `json:"origins"`
	UserAgent string `json:"user_agent"`
	UpdatedAt string `json:"updated_at"`
	Warning   string `json:"warning"`
}

// authRestoreResult is the redacted outcome of one auth_restore.
type authRestoreResult struct {
	Site           string   `json:"site"`
	Name           string   `json:"name"`
	Cookies        int      `json:"cookies"`
	Origins        int      `json:"origins"`
	LocalStorage   int      `json:"local_storage"`
	SessionStorage int      `json:"session_storage"`
	Forced         bool     `json:"forced"`
	Warnings       []string `json:"warnings,omitempty"`
	Warning        string   `json:"warning"`
}

// authListResult is the redacted listing: one summary row per snapshot.
type authListResult struct {
	Snapshots []store.AuthSummary `json:"snapshots"`
	Count     int                 `json:"count"`
	Warning   string              `json:"warning"`
}

// PageAuthSave captures the live page's auth state for site under name and
// persists it. The result carries counts only; no cookie or storage value is
// ever returned.
func PageAuthSave(
	ctx context.Context,
	p *browsers.Page,
	site, name string,
) (string, error) {
	site, name, err := authTarget(site, name)
	if err != nil {
		return "", err
	}
	snap, err := p.CaptureAuth(ctx, site)
	if err != nil {
		return "", fmt.Errorf("auth_save: %w", err)
	}
	payload, err := json.Marshal(snap)
	if err != nil {
		return "", fmt.Errorf("auth_save: encode snapshot: %w", err)
	}
	s, err := openAuthStore(ctx)
	if err != nil {
		return "", fmt.Errorf("auth_save: %w", err)
	}
	defer s.Close()
	updated, err := s.SaveAuthSnapshot(ctx, site, name, payload)
	if err != nil {
		return "", fmt.Errorf("auth_save: %w", err)
	}
	out, err := json.Marshal(authSaveResult{
		Site:      site,
		Name:      name,
		Cookies:   len(snap.Cookies),
		Origins:   len(snap.Origins),
		UserAgent: snap.UserAgent,
		UpdatedAt: updated.UTC().Format(time.RFC3339Nano),
		Warning:   authStoreWarning,
	})
	if err != nil {
		return "", fmt.Errorf("auth_save: encode result: %w", err)
	}
	return string(out), nil
}

// PageAuthRestore writes the named snapshot into the page's browser context.
// A user-agent mismatch refuses unless force is true. The result carries counts
// and warnings only.
func PageAuthRestore(
	ctx context.Context,
	p *browsers.Page,
	site, name string,
	force bool,
) (string, error) {
	site, name, err := authTarget(site, name)
	if err != nil {
		return "", err
	}
	s, err := openAuthStore(ctx)
	if err != nil {
		return "", fmt.Errorf("auth_restore: %w", err)
	}
	defer s.Close()
	rec, err := s.LoadAuthSnapshot(ctx, site, name)
	if err != nil {
		return "", fmt.Errorf("auth_restore: %w", err)
	}
	var snap browsers.AuthSnapshot
	if err := json.Unmarshal(rec.Payload, &snap); err != nil {
		return "", fmt.Errorf("auth_restore: decode snapshot %s/%s: %w", site, name, err)
	}
	report, err := p.ApplyAuth(ctx, &snap, force)
	if err != nil {
		return "", fmt.Errorf("auth_restore: %w", err)
	}
	out, err := json.Marshal(authRestoreResult{
		Site:           site,
		Name:           name,
		Cookies:        report.Cookies,
		Origins:        report.Origins,
		LocalStorage:   report.LocalStorage,
		SessionStorage: report.SessionStorage,
		Forced:         force,
		Warnings:       report.Warnings,
		Warning:        authStoreWarning,
	})
	if err != nil {
		return "", fmt.Errorf("auth_restore: encode result: %w", err)
	}
	return string(out), nil
}

// PageAuthList lists the saved snapshots, optionally narrowed to site. The
// output never contains a cookie or storage value.
func PageAuthList(ctx context.Context, site string) (string, error) {
	site = strings.TrimSpace(site)
	if site != "" {
		normalized, err := browsers.NormalizeAuthSite(site)
		if err != nil {
			return "", fmt.Errorf("auth_list: %w", err)
		}
		site = normalized
	}
	s, err := openAuthStore(ctx)
	if err != nil {
		return "", fmt.Errorf("auth_list: %w", err)
	}
	defer s.Close()
	snapshots, err := s.ListAuthSnapshots(ctx, site)
	if err != nil {
		return "", fmt.Errorf("auth_list: %w", err)
	}
	if snapshots == nil {
		snapshots = []store.AuthSummary{}
	}
	out, err := json.Marshal(authListResult{
		Snapshots: snapshots,
		Count:     len(snapshots),
		Warning:   authStoreWarning,
	})
	if err != nil {
		return "", fmt.Errorf("auth_list: encode result: %w", err)
	}
	return string(out), nil
}

// authTarget normalizes the site and validates the name for the save/restore
// actions.
func authTarget(site, name string) (string, string, error) {
	normalized, err := browsers.NormalizeAuthSite(site)
	if err != nil {
		return "", "", err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", fmt.Errorf("auth: empty name — pass a snapshot name such as main")
	}
	return normalized, name, nil
}

// openAuthStore opens the state database. The database file is the persistence
// boundary across daemon restarts; callers close it.
func openAuthStore(ctx context.Context) (*store.Store, error) {
	path, err := authStateDBPath()
	if err != nil {
		return nil, err
	}
	return store.Open(ctx, path)
}

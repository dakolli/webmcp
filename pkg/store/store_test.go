package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)

	version, err := s.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version != 1 {
		t.Fatalf("fresh version = %d, want 1", version)
	}

	mode, err := s.JournalMode(ctx)
	if err != nil {
		t.Fatalf("JournalMode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	version, err = reopened.Version(ctx)
	if err != nil {
		t.Fatalf("Version after reopen: %v", err)
	}
	if version != 1 {
		t.Fatalf("reopened version = %d, want 1", version)
	}
}

func TestJSONColumnRequiresJSONB(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)

	ddl := "CREATE TABLE payloads (id INTEGER PRIMARY KEY, value " + jsonColumn("value") + ")"
	if _, err := s.db.ExecContext(ctx, ddl); err != nil {
		t.Fatalf("create payloads table: %v", err)
	}

	const payload = `{"ok":true}`

	if _, err := s.db.ExecContext(ctx, "INSERT INTO payloads (value) VALUES (jsonb(?))", payload); err != nil {
		t.Fatalf("insert via jsonb: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO payloads (value) VALUES (?)", payload); err == nil {
		t.Fatal("plain-text insert succeeded, want CHECK constraint failure")
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO payloads (value) VALUES (jsonb(?))", `{oops`); err == nil {
		t.Fatal("insert of invalid JSON succeeded, want failure")
	}
}

func TestOpenTightensDirectoryMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cfg")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	s, err := Open(context.Background(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0700 {
		t.Fatalf("dir mode = %o, want 0700", got)
	}
}

func TestOpenEscapesDSNPath(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state?#.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open with metacharacters in path: %v", err)
	}
	defer s.Close()

	if _, err := s.db.ExecContext(ctx, "CREATE TABLE t (x INTEGER)"); err != nil {
		t.Fatalf("write after escaped open: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat database file: %v", err)
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)

	if _, err := s.db.ExecContext(ctx, "UPDATE schema_version SET version = 999"); err != nil {
		t.Fatalf("inflate schema version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := Open(ctx, path)
	if err == nil {
		t.Fatal("Open succeeded on a newer schema, want refusal")
	}
	if !strings.Contains(err.Error(), "999") || !strings.Contains(err.Error(), "1") {
		t.Fatalf("refusal %q does not name both versions", err)
	}
}

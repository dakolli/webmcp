// Package store owns the embedded SQLite database at <config dir>/state.db.
//
// Threat model: the database is a local, plaintext file created with mode 0600
// and holding session material (cookies, tokens, captured traffic). Anyone who
// can read the file can impersonate those sessions; keep it on the local disk,
// never sync or share it, and delete it to revoke everything it holds.
//
// The store opens with WAL, a five-second busy timeout, foreign keys on, and
// NORMAL synchronous mode, and it runs forward-only migrations guarded by the
// schema_version table. JSON payload columns are declared BLOB and written
// through jsonb(?); see jsonColumn for the exact definition to reuse.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const (
	driverName = "sqlite"

	dsnSuffix = "?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"
)

// migration is one numbered schema step applied inside a single transaction.
type migration struct {
	version int
	name    string
	stmts   []string
}

// migrations is the ordered, forward-only schema history. Append new steps with
// the next version; never edit or remove a step that has shipped.
var migrations = []migration{
	{
		version: 1,
		name:    "schema_version",
		stmts: []string{
			`CREATE TABLE schema_version (
				version    INTEGER NOT NULL CHECK (version > 0),
				applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
			)`,
		},
	},
}

// Store is an open handle to the state database. It serializes writes through a
// single connection so two callers can never race the same transaction.
type Store struct {
	path string
	db   *sql.DB
}

// Open creates the database file under path when missing, opens it with the WAL
// pragmas, and brings the schema up to date. Opening an already-current database
// is a no-op.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("store: empty database path")
	}
	if err := prepareFile(path); err != nil {
		return nil, err
	}
	conn, err := dsn(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(driverName, conn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: connect to %s: %w", path, err)
	}
	s := &Store{path: path, db: db}
	if err := s.Migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// dsn builds the file: URI for path, percent-escaping anything that would
// otherwise be parsed as a query or fragment, and appends the fixed pragmas.
func dsn(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("store: resolve %s: %w", path, err)
	}
	u := url.URL{Scheme: "file", Path: abs}
	return u.String() + dsnSuffix, nil
}

// prepareFile creates the parent directory at 0700 and the database file at
// 0600 before the driver touches it, repairing loosened modes on each open.
func prepareFile(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("store: create directory for %s: %w", path, err)
	}
	if dir != "." {
		if err := os.Chmod(dir, 0700); err != nil {
			return fmt.Errorf("store: set mode on directory for %s: %w", path, err)
		}
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return fmt.Errorf("store: create %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("store: close new %s: %w", path, err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		return fmt.Errorf("store: set mode on %s: %w", path, err)
	}
	return nil
}

// Close releases the database connection.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("store: close %s: %w", s.path, err)
	}
	return nil
}

// Migrate applies every migration newer than the recorded schema version. Each
// step commits in its own transaction, so a second call is a no-op.
func (s *Store) Migrate(ctx context.Context) error {
	current, err := s.Version(ctx)
	if err != nil {
		return err
	}
	latest := migrations[len(migrations)-1].version
	if current > latest {
		return fmt.Errorf(
			"store: database schema version %d is newer than this build supports (%d); upgrade the binary or use a different database",
			current,
			latest,
		)
	}
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if err := s.apply(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// apply runs one migration and records its version in the same transaction.
func (s *Store) apply(ctx context.Context, m migration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin migration %d (%s): %w", m.version, m.name, err)
	}
	defer tx.Rollback()
	for _, stmt := range m.stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("store: migration %d (%s): %w", m.version, m.name, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_version (version) VALUES (?)", m.version); err != nil {
		return fmt.Errorf("store: record migration %d (%s): %w", m.version, m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migration %d (%s): %w", m.version, m.name, err)
	}
	return nil
}

// Version reports the highest applied migration, or zero for a fresh database.
func (s *Store) Version(ctx context.Context) (int, error) {
	var tables int
	err := s.db.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_version'",
	).Scan(&tables)
	if err != nil {
		return 0, fmt.Errorf("store: inspect schema_version: %w", err)
	}
	if tables == 0 {
		return 0, nil
	}
	var version int
	err = s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("store: read schema version: %w", err)
	}
	return version, nil
}

// JournalMode reads the journal mode back from the open database.
func (s *Store) JournalMode(ctx context.Context) (string, error) {
	var mode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return "", fmt.Errorf("store: read journal_mode: %w", err)
	}
	return mode, nil
}

// Tables lists the user tables in the database, sorted by name.
func (s *Store) Tables(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(
		ctx,
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name",
	)
	if err != nil {
		return nil, fmt.Errorf("store: list tables: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("store: scan table name: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate tables: %w", err)
	}
	return names, nil
}

// jsonColumn returns the column definition for a JSON payload stored as BLOB.
// The CHECK uses JSONB-only validation (mask 4), so a writer that forgets
// jsonb(?) and passes plain TEXT is rejected rather than silently accepted.
func jsonColumn(name string) string {
	return fmt.Sprintf("%s BLOB NOT NULL CHECK (json_valid(%s, 4))", name, name)
}

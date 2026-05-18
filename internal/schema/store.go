package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	// Pure-Go SQLite driver; registers itself with database/sql on import.
	_ "modernc.org/sqlite"
)

// Store persists the searchable schema index in SQLite with FTS5
// ranking. One row per SearchUnit in `units` (the canonical table) +
// one matching row in `units_fts` (the FTS5 index over `composed`).
//
// FTS5 is built into the modernc.org/sqlite driver — no CGo. BM25 is
// the default ranking function and is what the schema_search tool
// returns ordered by.
type Store struct {
	db *sql.DB
}

// OpenStore opens (or creates) the SQLite database at path and
// initialises the schema. Pass ":memory:" for tests.
func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec(initSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the underlying handle.
func (s *Store) Close() error { return s.db.Close() }

// initSQL is the one-shot DDL. Idempotent — safe to run on every open.
// The triggers keep units_fts in sync with units on INSERT/UPDATE/DELETE
// so callers only touch `units` directly.
const initSQL = `
CREATE TABLE IF NOT EXISTS units (
  name        TEXT PRIMARY KEY,
  kind        TEXT NOT NULL,
  parent_type TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  signature   TEXT NOT NULL DEFAULT '',
  sdl         TEXT NOT NULL DEFAULT '',
  composed    TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS units_fts USING fts5(
  name, kind, parent_type, description, signature, composed,
  content='units', content_rowid='rowid', tokenize='porter unicode61'
);

CREATE TRIGGER IF NOT EXISTS units_ai AFTER INSERT ON units BEGIN
  INSERT INTO units_fts(rowid, name, kind, parent_type, description, signature, composed)
  VALUES (new.rowid, new.name, new.kind, new.parent_type, new.description, new.signature, new.composed);
END;
CREATE TRIGGER IF NOT EXISTS units_ad AFTER DELETE ON units BEGIN
  INSERT INTO units_fts(units_fts, rowid, name, kind, parent_type, description, signature, composed)
  VALUES('delete', old.rowid, old.name, old.kind, old.parent_type, old.description, old.signature, old.composed);
END;
CREATE TRIGGER IF NOT EXISTS units_au AFTER UPDATE ON units BEGIN
  INSERT INTO units_fts(units_fts, rowid, name, kind, parent_type, description, signature, composed)
  VALUES('delete', old.rowid, old.name, old.kind, old.parent_type, old.description, old.signature, old.composed);
  INSERT INTO units_fts(rowid, name, kind, parent_type, description, signature, composed)
  VALUES (new.rowid, new.name, new.kind, new.parent_type, new.description, new.signature, new.composed);
END;
`

// Replace atomically swaps the entire index for a fresh set of units.
// Used after a successful introspection refresh — the upstream schema
// is the source of truth, and partial diff-merge is more complexity
// than v0 needs.
func (s *Store) Replace(ctx context.Context, units []SearchUnit) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "DELETE FROM units"); err != nil {
		return fmt.Errorf("clear units: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO units (name, kind, parent_type, description, signature, sdl, composed)
VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, u := range units {
		if _, err := stmt.ExecContext(ctx,
			u.Name, u.Kind, u.ParentType, u.Description, u.Signature, u.SDL, u.Composed,
		); err != nil {
			return fmt.Errorf("insert %s: %w", u.Name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// SearchResult is one ranked hit from Search.
type SearchResult struct {
	Name        string
	Kind        string
	ParentType  string
	Description string
	Signature   string
	Score       float64
}

// Search runs a BM25-ranked FTS5 query over the index and returns the
// top `limit` hits. The query is wrapped so FTS5 treats it as a
// MATCH expression — callers pass plain natural-language strings.
//
// limit is clamped to [1, 50] to keep responses agent-context-budget
// friendly; 0 or negative defaults to 10.
func (s *Store) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	q := ftsQuery(query)
	if q == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT u.name, u.kind, u.parent_type, u.description, u.signature, bm25(units_fts) AS score
FROM units_fts
JOIN units u ON u.rowid = units_fts.rowid
WHERE units_fts MATCH ?
ORDER BY score
LIMIT ?`, q, limit)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.Name, &r.Kind, &r.ParentType, &r.Description, &r.Signature, &r.Score); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ErrNotFound is returned by GetSDL when no unit matches the given name.
var ErrNotFound = errors.New("schema unit not found")

// GetSDL returns the SDL fragment for a named unit. Accepts either a
// bare type name ("Customer") or a "Type.field" reference. Returns
// ErrNotFound if no match exists.
func (s *Store) GetSDL(ctx context.Context, name string) (string, error) {
	var sdl string
	err := s.db.QueryRowContext(ctx, "SELECT sdl FROM units WHERE name = ?", name).Scan(&sdl)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get sdl: %w", err)
	}
	return sdl, nil
}

// Count returns the number of indexed units. Used by tools and tests
// to assert the index is populated.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM units").Scan(&n)
	return n, err
}

// SetMeta upserts a key/value pair into the meta table. Used to
// persist the full SDL, last-refreshed timestamp, etc.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		key, value)
	return err
}

// GetMeta returns the stored value for key, or "" + ErrNotFound when
// absent. Callers treat empty as "not yet populated".
func (s *Store) GetMeta(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

// ftsQuery sanitises a user query for FTS5 MATCH. FTS5 has its own
// query syntax (AND/OR/NEAR, double-quoted phrases, column filters);
// agents pass natural language, so we strip operators and quote each
// token to force literal matching. Empty / whitespace-only input
// returns "" so Search short-circuits without hitting the DB.
func ftsQuery(s string) string {
	var out []byte
	var token []byte
	flush := func() {
		if len(token) == 0 {
			return
		}
		if len(out) > 0 {
			out = append(out, ' ')
		}
		out = append(out, '"')
		out = append(out, token...)
		out = append(out, '"')
		out = append(out, '*')
		token = token[:0]
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			flush()
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '.':
			token = append(token, c)
		}
	}
	flush()
	return string(out)
}

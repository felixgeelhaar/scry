// Package pq implements scry's persisted-queries store: a per-server
// SHA-256(query) → query text + friendly name map. Operators register
// expensive queries once via `scry pq add`; agents call
// `query_execute(hash="…")` instead of pushing the full query string
// every time.
//
// Storage: one SQLite file per server under <IndexDir>/<safe>.pq.db,
// kept separate from the schema index so a `pq` refresh doesn't
// invalidate the FTS5 index and vice versa.
package pq

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	// Pure-Go SQLite driver. The schema package already pulls it
	// in but the import is per-package — Go's import-tracking
	// scopes blank imports to the importing file.
	_ "modernc.org/sqlite"
)

// Hash returns the canonical persisted-query hash for a query
// string. SHA-256 hex over the raw bytes; deterministic across Go
// versions + platforms.
func Hash(query string) string {
	h := sha256.Sum256([]byte(query))
	return hex.EncodeToString(h[:])
}

// Store persists registered queries for one upstream. Safe for
// concurrent use — every method takes an explicit context + serialises
// against SQLite's connection pool.
type Store struct {
	db *sql.DB
}

// OpenStore opens (or creates) the SQLite database at path with the
// canonical schema. Use a `:memory:` path in tests.
func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open pq store: %w", err)
	}
	if _, err := db.Exec(initSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init pq schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the underlying handle.
func (s *Store) Close() error { return s.db.Close() }

const initSQL = `
CREATE TABLE IF NOT EXISTS persisted (
  hash  TEXT PRIMARY KEY,
  name  TEXT NOT NULL UNIQUE,
  query TEXT NOT NULL
);
`

// ErrNotFound is returned by Get when no entry matches the supplied
// hash or name.
var ErrNotFound = errors.New("pq: persisted query not found")

// Entry is one registered query.
type Entry struct {
	Hash  string
	Name  string
	Query string
}

// Put registers (or overwrites) a query under the given friendly
// name. The hash is computed deterministically; callers can re-derive
// it via Hash(query).
//
// Overwrite semantics: existing name → replaces the query (and the
// hash, since the query bytes changed). Existing hash with a
// different name → also replaces. Both keys point at the latest
// query; agents that cached an old hash get a clean ErrNotFound.
func (s *Store) Put(ctx context.Context, name, query string) (Entry, error) {
	if name == "" {
		return Entry{}, errors.New("pq: name is required")
	}
	if query == "" {
		return Entry{}, errors.New("pq: query is required")
	}
	hash := Hash(query)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Entry{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// Delete any prior entry under this name (query bytes likely
	// changed → new hash) so the unique-name constraint stays
	// satisfied.
	if _, err := tx.ExecContext(ctx, "DELETE FROM persisted WHERE name = ?", name); err != nil {
		return Entry{}, fmt.Errorf("clear prior: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT OR REPLACE INTO persisted(hash, name, query) VALUES (?, ?, ?)",
		hash, name, query,
	); err != nil {
		return Entry{}, fmt.Errorf("insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Entry{}, err
	}
	return Entry{Hash: hash, Name: name, Query: query}, nil
}

// GetByHash returns the entry for one hash. ErrNotFound when absent.
func (s *Store) GetByHash(ctx context.Context, hash string) (Entry, error) {
	var e Entry
	row := s.db.QueryRowContext(ctx, "SELECT hash, name, query FROM persisted WHERE hash = ?", hash)
	if err := row.Scan(&e.Hash, &e.Name, &e.Query); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Entry{}, ErrNotFound
		}
		return Entry{}, fmt.Errorf("scan: %w", err)
	}
	return e, nil
}

// GetByName returns the entry for one friendly name. ErrNotFound
// when absent.
func (s *Store) GetByName(ctx context.Context, name string) (Entry, error) {
	var e Entry
	row := s.db.QueryRowContext(ctx, "SELECT hash, name, query FROM persisted WHERE name = ?", name)
	if err := row.Scan(&e.Hash, &e.Name, &e.Query); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Entry{}, ErrNotFound
		}
		return Entry{}, fmt.Errorf("scan: %w", err)
	}
	return e, nil
}

// Delete removes an entry by hash OR name. Returns ErrNotFound when
// neither matches.
func (s *Store) Delete(ctx context.Context, hashOrName string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM persisted WHERE hash = ? OR name = ?", hashOrName, hashOrName)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// List returns every entry sorted alphabetically by name — stable
// output for `scry pq list` + tests.
func (s *Store) List(ctx context.Context) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT hash, name, query FROM persisted ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Hash, &e.Name, &e.Query); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, rows.Err()
}

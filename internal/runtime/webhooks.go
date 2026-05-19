package runtime

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	// Pure-Go SQLite driver — registers itself with database/sql
	// on import. Per-package blank import (Go scopes blank imports
	// to the importing file).
	_ "modernc.org/sqlite"
)

// WebhookStore is the per-server registry of schema-diff webhook
// subscriptions. Persisted at <IndexDir>/<safe>.webhooks.db (sqlite)
// so registrations survive scry restarts.
//
// Schema:
//
//	id          INTEGER PRIMARY KEY AUTOINCREMENT
//	url         TEXT NOT NULL
//	secret      TEXT NOT NULL  -- 32-byte hex; HMAC-SHA256 sign key
//	created_at  TEXT NOT NULL  -- RFC 3339 UTC timestamp
//
// The secret is returned ONCE on registration. After that, only the
// id is exposed via list/remove — operators that lose the secret
// must remove + re-register.
type WebhookStore struct {
	db *sql.DB
}

// OpenWebhookStore opens (or creates) the per-server webhooks DB.
// Use a `:memory:` path in tests.
func OpenWebhookStore(path string) (*WebhookStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open webhooks store: %w", err)
	}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS webhooks (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  url        TEXT NOT NULL,
  secret     TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_webhooks_url ON webhooks(url);
`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init webhooks schema: %w", err)
	}
	return &WebhookStore{db: db}, nil
}

// Close releases the underlying handle.
func (s *WebhookStore) Close() error { return s.db.Close() }

// Webhook is one registration row.
type Webhook struct {
	ID        int64
	URL       string
	Secret    string // 32-byte hex; only set on Register's return value
	CreatedAt time.Time
}

// ErrWebhookNotFound is returned by Remove when the id doesn't exist.
var ErrWebhookNotFound = errors.New("webhooks: registration not found")

// Register adds a webhook URL + mints a fresh HMAC secret. Returns
// the full row including the secret — caller MUST surface it to the
// operator on this call only; subsequent List calls won't include
// the secret.
//
// URL is stored verbatim. Operator validation (https://, host
// allowlist) is the registering tool's responsibility, not the
// store's.
func (s *WebhookStore) Register(ctx context.Context, url string) (Webhook, error) {
	if url == "" {
		return Webhook{}, errors.New("webhooks: url is required")
	}
	secret, err := mintSecret()
	if err != nil {
		return Webhook{}, fmt.Errorf("mint secret: %w", err)
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO webhooks(url, secret, created_at) VALUES (?, ?, ?)",
		url, secret, now.Format(time.RFC3339))
	if err != nil {
		return Webhook{}, fmt.Errorf("insert webhook: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Webhook{}, fmt.Errorf("last insert id: %w", err)
	}
	return Webhook{ID: id, URL: url, Secret: secret, CreatedAt: now}, nil
}

// List returns every registration, ordered by id ascending. Secret
// is intentionally left empty — only Register exposes it.
func (s *WebhookStore) List(ctx context.Context) ([]Webhook, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, url, created_at FROM webhooks ORDER BY id ASC")
	if err != nil {
		return nil, fmt.Errorf("list webhooks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Webhook
	for rows.Next() {
		var w Webhook
		var created string
		if err := rows.Scan(&w.ID, &w.URL, &created); err != nil {
			return nil, fmt.Errorf("scan webhook: %w", err)
		}
		t, _ := time.Parse(time.RFC3339, created)
		w.CreatedAt = t
		out = append(out, w)
	}
	return out, rows.Err()
}

// Remove deletes one registration by id. Returns ErrWebhookNotFound
// when no row matches.
func (s *WebhookStore) Remove(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM webhooks WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete webhook: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrWebhookNotFound
	}
	return nil
}

// Forward returns every registration INCLUDING the secret. Used by
// the dispatcher to sign outgoing webhook bodies; never exposed to
// MCP tools.
func (s *WebhookStore) Forward(ctx context.Context) ([]Webhook, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, url, secret, created_at FROM webhooks ORDER BY id ASC")
	if err != nil {
		return nil, fmt.Errorf("forward webhooks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Webhook
	for rows.Next() {
		var w Webhook
		var created string
		if err := rows.Scan(&w.ID, &w.URL, &w.Secret, &created); err != nil {
			return nil, fmt.Errorf("scan webhook: %w", err)
		}
		t, _ := time.Parse(time.RFC3339, created)
		w.CreatedAt = t
		out = append(out, w)
	}
	return out, rows.Err()
}

// mintSecret returns 32 cryptographically-random bytes hex-encoded.
// Used as the HMAC-SHA256 sign key for outgoing webhook payloads.
func mintSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

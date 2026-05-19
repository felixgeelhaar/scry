// Storage-backend abstraction for the schema index. v0.7 ships the
// interface alongside the existing SQLite-backed Store; v0.8 will
// add a postgres + pgvector implementation for multi-instance HA
// deployments. SQLite stays the default; operators opt into pg via
// a future servers.yml `storage:` block.
//
// Interface-only design rather than a runtime swap keeps the v0.7
// ship surface tight + lets callers continue using *schema.Store
// unchanged until v0.8 renames to *schema.SQLiteStore +
// introduces *schema.PGStore.

package schema

import "context"

// Index is the storage-backend interface the runtime depends on.
// Every method that *Store exposes today is part of the contract.
// A v0.8 PGStore (pgvector + pg-FTS) satisfies the same interface;
// the runtime takes the interface, not the concrete type.
//
// Note: this interface is currently UNUSED at call sites — the
// runtime still depends on *Store directly. Migrating callers
// behind the interface (a small refactor across runtime/manager.go
// + every test fixture) lands when the PG implementation arrives.
// Compile-time assertion below guarantees *Store satisfies the
// contract so the interface stays in sync with the concrete type.
type Index interface {
	Replace(ctx context.Context, units []SearchUnit) error
	Search(ctx context.Context, query string, limit int) ([]SearchResult, error)
	GetSDL(ctx context.Context, name string) (string, error)
	Count(ctx context.Context) (int, error)
	SetMeta(ctx context.Context, key, value string) error
	GetMeta(ctx context.Context, key string) (string, error)
	ReplaceNeighbors(ctx context.Context, edges []Edge) error
	Neighbors(ctx context.Context, name string, limit int) (NeighborSet, error)
	Close() error
}

// Compile-time guarantee that *Store satisfies Index. Surfaces any
// new method or signature drift on Store at build time, not at
// PGStore-integration time in v0.8.
var _ Index = (*Store)(nil)

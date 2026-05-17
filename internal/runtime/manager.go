// Package runtime owns scry's per-upstream state: the schema index,
// the fortify-wrapped HTTP client, and the credential resolver hook
// for each configured GraphQL server. One Manager per process; each
// MCP tool resolves its target server through it.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	otelmetric "go.opentelemetry.io/otel/metric"

	"github.com/felixgeelhaar/scry/internal/auth"
	"github.com/felixgeelhaar/scry/internal/obs"
	"github.com/felixgeelhaar/scry/internal/schema"
	"github.com/felixgeelhaar/scry/internal/upstream"
)

// otelmetricInstrument is a tiny helper to keep the verbose
// `otelmetric.WithAttributes(attribute.String(...))` pattern off
// the call sites. Returns the option set callers pass to
// counter.Add / histogram.Record.
func otelmetricInstrument(key, value string) otelmetric.AddOption {
	return otelmetric.WithAttributes(attribute.String(key, value))
}

// Entry is the resolved state for one upstream. Each MCP tool
// dispatches against an Entry's Store + Client.
type Entry struct {
	Name     string
	Upstream string
	Store    *schema.Store
	Client   *upstream.Client
	// AuthRef is the *reference* used to resolve the upstream
	// token at request time, kept for logs / status. Never holds
	// the resolved secret itself.
	AuthRef string
	// SDLPath, when non-empty, signals that this entry's schema
	// was loaded from a local SDL file rather than via
	// introspection. Refresh re-reads the same file.
	SDLPath string
}

// Manager owns every active upstream. Read-mostly: servers are
// loaded at boot then looked up by name. Future hot-reload via
// fsnotify on servers.yml will add Replace() under a write lock.
type Manager struct {
	mu      sync.RWMutex
	entries map[string]*Entry
	// IndexDir is where each Entry's SQLite file lives. Files are
	// named <safe-host>.db so multiple upstreams don't collide.
	IndexDir string
	// CostCeiling propagates to per-tool gating (set in server.Config).
	// Kept here so the Manager owns "what to do" config the tools
	// need at call time.
	CostCeiling int
}

// New creates an empty Manager. Caller seeds entries via Load or
// AddFromConfig and Closes when done.
func New(indexDir string, costCeiling int) (*Manager, error) {
	if indexDir == "" {
		return nil, errors.New("runtime: IndexDir is required")
	}
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		return nil, fmt.Errorf("runtime: mkdir index dir: %w", err)
	}
	return &Manager{
		entries:     map[string]*Entry{},
		IndexDir:    indexDir,
		CostCeiling: costCeiling,
	}, nil
}

// Close releases every Entry's store handle. Safe to call multiple
// times; subsequent calls are no-ops.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var firstErr error
	for _, e := range m.entries {
		if err := e.Store.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	m.entries = nil
	return firstErr
}

// Get returns the Entry for name. Returns ErrUnknownServer when no
// upstream with that name is configured — tool handlers translate
// that to a structured `unknown_server` envelope.
func (m *Manager) Get(name string) (*Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[name]
	if !ok {
		return nil, ErrUnknownServer
	}
	return e, nil
}

// List returns server names in alphabetical order. Used by the
// list_servers tool + CLI status output.
func (m *Manager) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.entries))
	for n := range m.entries {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// DefaultServer returns the only server's name when exactly one is
// configured (the legacy --upstream path). Empty string + false
// when zero or many are configured — handlers in multi-server mode
// must require the caller to be explicit.
func (m *Manager) DefaultServer() (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.entries) == 1 {
		for n := range m.entries {
			return n, true
		}
	}
	return "", false
}

// ErrUnknownServer is returned by Get when the caller names a
// server the Manager has no Entry for.
var ErrUnknownServer = errors.New("runtime: unknown server")

// AddConfig describes one upstream the Manager should host. Used by
// both the legacy --upstream path (synthesised in-memory) and the
// servers.yml loader.
type AddConfig struct {
	Name     string
	Upstream string
	AuthRef  string // env://... / file://... / op://... / literal
	// Force forces a fresh introspection even when the per-server
	// SQLite index already has units cached. Used by the
	// hot-reload path when an upstream URL changes — same file
	// name, different schema, so the cache is stale.
	Force bool
	// SDLPath, when non-empty, skips introspection entirely and
	// loads the schema from a checked-in SDL file. Right for
	// upstreams that disable introspection or reject our
	// introspection query. Hot reload re-loads the SDL when the
	// path changes (same diff machinery as upstream URL change).
	SDLPath string
}

// Add wires one upstream: opens its store, builds its fortify
// client, runs the initial introspection. Reused by Load (multi)
// and the single-upstream bootstrap path so introspection
// semantics are identical in both modes.
func (m *Manager) Add(ctx context.Context, ac AddConfig) error {
	if ac.Name == "" {
		return errors.New("runtime: server name is required")
	}
	if ac.Upstream == "" {
		return fmt.Errorf("runtime: server %q: upstream URL is required", ac.Name)
	}
	store, err := schema.OpenStore(filepath.Join(m.IndexDir, safeIndexName(ac.Name)+".db"))
	if err != nil {
		return fmt.Errorf("runtime: open store for %q: %w", ac.Name, err)
	}

	resolver := tokenResolver(ac.Name, ac.AuthRef)
	client, err := upstream.New(upstream.Config{
		Endpoint: ac.Upstream,
		Auth:     resolver,
	})
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("runtime: build upstream client for %q: %w", ac.Name, err)
	}

	entry := &Entry{
		Name:     ac.Name,
		Upstream: ac.Upstream,
		Store:    store,
		Client:   client,
		AuthRef:  ac.AuthRef,
		SDLPath:  ac.SDLPath,
	}

	if err := refreshEntry(ctx, entry, resolver, ac.Force); err != nil {
		_ = store.Close()
		return fmt.Errorf("runtime: refresh %q: %w", ac.Name, err)
	}

	m.mu.Lock()
	m.entries[ac.Name] = entry
	m.mu.Unlock()
	return nil
}

// addConfigFromServer translates one auth.Server into an AddConfig.
// Centralised so the YAML→Manager mapping stays in one place — keeps
// the Replace + LoadFromServers paths consistent.
func addConfigFromServer(name string, srv auth.Server) AddConfig {
	return AddConfig{
		Name:     name,
		Upstream: srv.Upstream,
		AuthRef:  srv.Auth.Token,
		SDLPath:  srv.SDLPath,
	}
}

// LoadFromServers seeds the Manager from a parsed servers.yml.
// Returns aggregated errors so one mistyped upstream doesn't kill
// all of the others — operators see what failed and can correct it
// without restarting the world.
func (m *Manager) LoadFromServers(ctx context.Context, s *auth.Servers) error {
	if s == nil {
		return errors.New("runtime: nil Servers")
	}
	var errs []error
	for name, srv := range s.Servers {
		if err := m.Add(ctx, addConfigFromServer(name, srv)); err != nil {
			obs.L.Error().
				Str("event", "runtime.add_failed").
				Str("server", name).
				Err(err).
				Msg("could not register upstream from servers.yml")
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 && len(m.entries) == 0 {
		return errors.Join(errs...)
	}
	return nil
}

// Replace computes the diff between the current entries and a freshly
// parsed servers.yml, then applies it minimally:
//
//   - server present in s, absent in entries  → Add (introspect, store, client)
//   - server absent in s, present in entries  → Close, drop
//   - same name, upstream URL changed         → Close + re-Add (URL change
//     invalidates the cached index)
//   - same name, only auth token ref changed  → SetAuth on the existing client
//     (preserves index + circuit-breaker state; no re-introspection)
//
// Aggregated errors are returned but partial successes are kept so
// one mistyped entry doesn't roll the whole set back.
func (m *Manager) Replace(ctx context.Context, s *auth.Servers) error {
	if s == nil {
		return errors.New("runtime: nil Servers")
	}

	m.mu.Lock()
	current := make(map[string]*Entry, len(m.entries))
	for k, v := range m.entries {
		current[k] = v
	}
	m.mu.Unlock()

	var errs []error

	// Apply changes for every server in the new config.
	for name, srv := range s.Servers {
		existing, ok := current[name]
		if !ok {
			if err := m.Add(ctx, AddConfig{
				Name:     name,
				Upstream: srv.Upstream,
				AuthRef:  srv.Auth.Token,
			}); err != nil {
				obs.L.Error().
					Str("event", "runtime.replace_add_failed").
					Str("server", name).
					Err(err).
					Msg("hot-reload: could not register new upstream")
				errs = append(errs, err)
			} else {
				obs.L.Info().
					Str("event", "runtime.replace_added").
					Str("server", name).
					Msg("hot-reload: added upstream")
			}
			continue
		}
		// Same name — decide whether URL changed (full reload)
		// or just the token ref (cheap swap).
		if existing.Upstream != srv.Upstream {
			m.removeUnsafe(name)
			if err := m.Add(ctx, AddConfig{
				Name:     name,
				Upstream: srv.Upstream,
				AuthRef:  srv.Auth.Token,
				Force:    true, // URL change: cached schema is stale
			}); err != nil {
				obs.L.Error().
					Str("event", "runtime.replace_readd_failed").
					Str("server", name).
					Err(err).
					Msg("hot-reload: upstream URL changed but re-add failed")
				errs = append(errs, err)
			} else {
				obs.L.Info().
					Str("event", "runtime.replace_url_changed").
					Str("server", name).
					Msg("hot-reload: upstream URL changed; re-introspected")
			}
			continue
		}
		if existing.AuthRef != srv.Auth.Token {
			existing.AuthRef = srv.Auth.Token
			existing.Client.SetAuth(tokenResolver(name, srv.Auth.Token))
			obs.L.Info().
				Str("event", "runtime.replace_token_rotated").
				Str("server", name).
				Str("new_ref", obs.RedactTokenRef(srv.Auth.Token)).
				Msg("hot-reload: token rotated without re-introspection")
		}
	}

	// Drop servers that disappeared from the config.
	for name := range current {
		if _, kept := s.Servers[name]; !kept {
			m.removeUnsafe(name)
			obs.L.Info().
				Str("event", "runtime.replace_removed").
				Str("server", name).
				Msg("hot-reload: removed upstream")
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// removeUnsafe deletes one entry and closes its store. Holds the
// write lock for the duration. Used by Replace; not exported because
// callers should go through Replace for diff-driven changes.
func (m *Manager) removeUnsafe(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[name]
	if !ok {
		return
	}
	_ = e.Store.Close()
	delete(m.entries, name)
}

// Refresh re-introspects a single server. Called by the background
// refresher goroutine on each tick.
func (m *Manager) Refresh(ctx context.Context, name string) error {
	entry, err := m.Get(name)
	if err != nil {
		return err
	}
	resolver := tokenResolver(entry.Name, entry.AuthRef)
	return refreshEntry(ctx, entry, resolver, true)
}

// refreshEntry runs introspection + index rebuild for one Entry.
// `force` decides whether to refresh even when the cached index is
// non-empty; boot-time callers pass false so a warm cache survives
// restarts.
func refreshEntry(ctx context.Context, e *Entry, resolver func() string, force bool) error {
	ctx, span := otel.Tracer("github.com/felixgeelhaar/scry").Start(ctx, "runtime.refresh")
	defer span.End()
	span.SetAttributes(
		attribute.String("server", e.Name),
		attribute.String("upstream", e.Upstream),
		attribute.Bool("force", force),
	)

	n, err := e.Store.Count(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "count")
		return fmt.Errorf("count units: %w", err)
	}
	if n > 0 && !force {
		span.SetAttributes(attribute.Bool("skipped_warm_cache", true))
		obs.L.Debug().
			Str("event", "runtime.refresh_skipped_warm_cache").
			Str("server", e.Name).
			Int("units", n).
			Msg("cached index already populated; skipping initial introspection")
		return nil
	}

	// SDL-file path: skip the introspection round-trip entirely.
	// Right for upstreams that disable introspection or sit
	// behind CDNs that reject our query at any depth.
	if e.SDLPath != "" {
		span.SetAttributes(attribute.Bool("sdl_file", true), attribute.String("sdl_path", e.SDLPath))
		s, err := schema.LoadSDLFile(e.SDLPath)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "load_sdl")
			return fmt.Errorf("load sdl file: %w", err)
		}
		if err := persistSchema(ctx, e, s, schema.IntrospectionMode("sdl")); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "persist_sdl")
			return err
		}
		obs.L.Info().
			Str("event", "runtime.sdl_loaded").
			Str("server", e.Name).
			Str("path", e.SDLPath).
			Int("types", len(s.Types)).
			Msg("loaded schema from SDL file (introspection skipped)")
		return nil
	}

	c := schema.NewClient(e.Upstream, resolver, &http.Client{Timeout: 30 * time.Second})
	ictx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	introspectStart := time.Now()
	s, mode, err := c.Introspect(ictx)
	m := obs.Metrics()
	if err != nil {
		m.IntrospectErrors.Add(ctx, 1, otelmetricInstrument("server", e.Name))
		span.RecordError(err)
		span.SetStatus(codes.Error, "introspect")
		// Keep serving the cached index on transient upstream
		// failures (matches the previous single-upstream
		// behaviour). Bubble up only when the index is empty
		// and there's nothing to fall back to.
		if n > 0 {
			obs.L.Warn().
				Str("event", "introspect.refresh_failed_cached_kept").
				Str("server", e.Name).
				Str("upstream", e.Upstream).
				Err(err).
				Msg("introspection refresh failed; serving cached index")
			return nil
		}
		return err
	}
	if mode == schema.IntrospectionShallow {
		obs.L.Warn().
			Str("event", "introspect.shallow_fallback").
			Str("server", e.Name).
			Str("upstream", e.Upstream).
			Msg("upstream rejected full-depth introspection; indexed with shallow query (inner NonNull wrappers may be lost in SDL)")
	}

	if err := persistSchema(ctx, e, s, mode); err != nil {
		return err
	}
	units := schema.BuildUnits(s)

	span.SetAttributes(
		attribute.String("introspection.mode", string(mode)),
		attribute.Int("types", len(s.Types)),
		attribute.Int("units", len(units)),
	)
	m.IntrospectCount.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("server", e.Name),
		attribute.String("mode", string(mode)),
	))
	m.UpstreamLatency.Record(ctx, time.Since(introspectStart).Seconds(), otelmetric.WithAttributes(
		attribute.String("server", e.Name),
		attribute.String("op", "introspect"),
	))
	obs.L.Info().
		Str("event", "introspect.refresh").
		Str("server", e.Name).
		Str("upstream", e.Upstream).
		Str("mode", string(mode)).
		Int("types", len(s.Types)).
		Int("units", len(units)).
		Msg("schema index refreshed")
	return nil
}

// tokenResolver wraps auth.ResolveToken in a closure that also logs
// resolution failures with the redacted reference. Used by both the
// upstream client (per-call) and the schema introspection client.
func tokenResolver(server, ref string) func() string {
	return func() string {
		tok, err := auth.ResolveToken(ref)
		if err != nil {
			obs.L.Error().
				Str("event", "auth.token_resolve_failed").
				Str("server", server).
				Str("ref", obs.RedactTokenRef(ref)).
				Err(err).
				Msg("upstream token resolution failed; sending no Authorization header")
			return ""
		}
		return tok
	}
}

// persistSchema is the tail half of refreshEntry — once a Schema is
// in hand (from either Introspect or LoadSDLFile) it indexes the
// units, writes the full SDL meta, and stamps the refresh timestamp.
// Extracted so both code paths share identical persistence semantics.
func persistSchema(ctx context.Context, e *Entry, s *schema.Schema, mode schema.IntrospectionMode) error {
	units := schema.BuildUnits(s)
	if err := e.Store.Replace(ctx, units); err != nil {
		return fmt.Errorf("replace units: %w", err)
	}
	if err := e.Store.SetMeta(ctx, "full_sdl", schema.BuildSDL(s)); err != nil {
		return fmt.Errorf("persist sdl: %w", err)
	}
	if err := e.Store.SetMeta(ctx, "refreshed_at", time.Now().UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("persist refresh time: %w", err)
	}
	if err := e.Store.SetMeta(ctx, "introspection_mode", string(mode)); err != nil {
		return fmt.Errorf("persist introspection mode: %w", err)
	}
	return nil
}

// safeIndexName escapes a server name into something usable as a
// filesystem leaf — replaces path separators and other troublesome
// characters with underscores. Operators name servers casually
// ("shopify-staging", "internal/api"); we can't trust the value.
func safeIndexName(name string) string {
	out := make([]byte, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_':
			out[i] = c
		default:
			out[i] = '_'
		}
	}
	return string(out)
}

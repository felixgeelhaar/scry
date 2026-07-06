// Package server bootstraps the scry MCP server: parses flags,
// builds the runtime.Manager (which owns the per-upstream schema
// index + fortify-wrapped client), registers MCP tools, and serves
// on the chosen transport.
package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	mcp "go.klarlabs.de/mcp"
	"go.klarlabs.de/mcp/transport"

	"github.com/felixgeelhaar/scry/internal/auth"
	"github.com/felixgeelhaar/scry/internal/gate"
	"github.com/felixgeelhaar/scry/internal/obs"
	"github.com/felixgeelhaar/scry/internal/runtime"
)

// Config is the resolved CLI configuration for one scry invocation.
//
// Two upstream-discovery modes:
//
//   - Single (legacy): --upstream + --auth flags. scry synthesises
//     an in-memory server named "default" and serves only that one.
//     Right for desktop MCP clients launching scry as a subprocess.
//
//   - Multi (hosted): no --upstream flag. scry loads every server
//     from $XDG_CONFIG_HOME/scry/servers.yml. Right for company-
//     hosted MCP serving multiple agents across multiple upstreams.
type Config struct {
	// Single-upstream flags.
	UpstreamURL string
	AuthToken   string
	// SDLFile, when non-empty, loads the upstream's schema from
	// the given file instead of running introspection. Right for
	// upstreams that disable introspection or sit behind CDNs that
	// reject our query.
	SDLFile string

	// IndexDir is where each upstream's SQLite index file lives.
	// Defaults to $XDG_DATA_HOME/scry/. Each server gets one file
	// named <safe-server-name>.db so multiple upstreams never
	// collide.
	IndexDir string

	// RefreshInterval controls how often the background refresher
	// re-introspects every configured upstream. Zero disables.
	// Production default 24h.
	RefreshInterval time.Duration

	// CostCeiling rejects query_execute calls whose estimated
	// complexity exceeds this threshold. 0 disables.
	CostCeiling int
	// CacheTTL caps how long a cached read-query result stays
	// fresh. 0 disables the cache. Default 30s — enough to dedupe
	// rapid agent re-queries within a single task without serving
	// stale data across tasks.
	CacheTTL time.Duration
	// CacheMaxEntries caps the per-upstream cache. 0 = unlimited
	// (TTL is the only eviction signal). Default 1000.
	CacheMaxEntries int
	// SessionWriteLimit caps mutations per session (gate.Policy).
	// 0 disables.
	SessionWriteLimit int
	// SessionComplexityLimit caps cumulative query complexity per
	// session (gate.Policy). 0 disables.
	SessionComplexityLimit int
	// EvidenceLimit caps the in-memory audit chain per session.
	// 0 = unbounded. When AuditDir is set the on-disk chain is
	// always unbounded; this limit only applies to the in-memory
	// window.
	EvidenceLimit int
	// AuditDir, when non-empty, persists each session's evidence
	// chain to `<dir>/<safe-session>.jsonl`. Replayed on boot so
	// VerifyChain survives restarts.
	AuditDir string
	// AuditMaxSize caps individual JSONL file size in bytes
	// before rotation. 0 disables (single growing file).
	AuditMaxSize int64
	// AuditKeep caps archived rotation files per session. 0
	// retains all archives forever.
	AuditKeep int

	// Transport selects the MCP transport: stdio | http | grpc | ws.
	Transport string
	// ListenAddr is required for non-stdio transports.
	ListenAddr string
	// ServeAuthToken is the admin transport-level shared secret —
	// holders may call every tool, including the destructive ones
	// (query_execute, auth_login). Distinct from AuthToken (which
	// scry uses to talk to the upstream).
	ServeAuthToken string
	// ServeAuthTokenReadOnly is a *second* transport credential
	// scoped to non-destructive tools only: list_servers,
	// schema_search, schema_get, query_validate, query_cost,
	// auth_status. Right for dashboards / discovery agents that
	// shouldn't be able to mutate upstream data.
	ServeAuthTokenReadOnly string

	// TLSCertFile + TLSKeyFile enable embedded TLS termination on
	// http/grpc/ws transports. Empty disables (operator runs an
	// edge proxy instead). Both must be set together; either alone
	// is a config error.
	TLSCertFile string
	TLSKeyFile  string
	// MTLSCAFile, when non-empty, enables mTLS: clients must
	// present a cert signed by one of the CAs in this PEM bundle.
	// Layers on top of TLS; meaningless without TLSCertFile/Key.
	MTLSCAFile string
}

// ParseFlags parses the `serve` subcommand's flags.
func ParseFlags(args []string) (Config, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfg := Config{}
	fs.StringVar(&cfg.UpstreamURL, "upstream", "", "GraphQL upstream URL — single-upstream mode. Omit to load every server from servers.yml.")
	fs.StringVar(&cfg.AuthToken, "auth", "", "bearer token (or env://VAR / file://path / op://...) for the single-upstream mode")
	fs.StringVar(&cfg.SDLFile, "sdl-file", "", "load schema from this SDL file instead of introspection (single-upstream mode)")
	fs.StringVar(&cfg.IndexDir, "index", "", "directory holding the schema index (defaults to ~/.local/share/scry)")
	fs.DurationVar(&cfg.RefreshInterval, "refresh-interval", 24*time.Hour, "background refresh cadence; 0 disables")
	fs.IntVar(&cfg.CostCeiling, "cost-ceiling", 1000, "reject query_execute calls whose estimated complexity exceeds this; 0 disables")
	fs.DurationVar(&cfg.CacheTTL, "cache-ttl", 30*time.Second, "per-upstream read-query result cache TTL; 0 disables caching")
	fs.IntVar(&cfg.CacheMaxEntries, "cache-max-entries", 1000, "per-upstream cache capacity; 0 = unbounded")
	fs.IntVar(&cfg.SessionWriteLimit, "session-writes", 0, "cap mutations per session; 0 = unlimited")
	fs.IntVar(&cfg.SessionComplexityLimit, "session-complexity", 0, "cap cumulative query complexity per session; 0 = unlimited")
	fs.IntVar(&cfg.EvidenceLimit, "evidence-limit", 1000, "cap in-memory audit chain per session; 0 = unbounded")
	fs.StringVar(&cfg.AuditDir, "audit-dir", "", "directory to persist evidence chains; empty disables on-disk audit")
	fs.Int64Var(&cfg.AuditMaxSize, "audit-max-size", 50<<20, "rotate per-session audit JSONL above this size in bytes (50 MiB default); 0 disables rotation")
	fs.IntVar(&cfg.AuditKeep, "audit-keep", 5, "retain at most this many archived audit files per session; 0 retains all")
	fs.StringVar(&cfg.Transport, "transport", "stdio", "MCP transport: stdio | http | grpc | ws")
	fs.StringVar(&cfg.ListenAddr, "listen", ":7777", "listen address (used by http/grpc/ws transports)")
	fs.StringVar(&cfg.ServeAuthToken, "serve-auth", "", "admin bearer token clients must present; accepts env://VAR / file://path / op://... refs.")
	fs.StringVar(&cfg.ServeAuthTokenReadOnly, "serve-auth-readonly", "", "additional bearer token granting read-only access (no query_execute / auth_login); same ref schemes as --serve-auth")
	fs.StringVar(&cfg.TLSCertFile, "tls-cert", "", "PEM-encoded TLS certificate (enables embedded TLS on http/grpc/ws)")
	fs.StringVar(&cfg.TLSKeyFile, "tls-key", "", "PEM-encoded TLS private key (required when --tls-cert is set)")
	fs.StringVar(&cfg.MTLSCAFile, "mtls-ca", "", "PEM bundle of CAs to verify client certs against (enables mTLS; requires --tls-cert)")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Run boots the MCP server, builds the runtime.Manager, seeds it
// from --upstream OR servers.yml, registers tools, kicks off the
// background refresher, and serves until ctx cancels.
func Run(ctx context.Context, cfg Config) error {
	indexDir, err := resolveIndexDir(cfg)
	if err != nil {
		return fmt.Errorf("resolve index dir: %w", err)
	}
	mgr, err := runtime.New(indexDir, cfg.CostCeiling)
	if err != nil {
		return err
	}
	mgr.CacheTTL = cfg.CacheTTL
	mgr.CacheMaxEntries = cfg.CacheMaxEntries
	defer func() { _ = mgr.Close() }()

	if err := seedManager(ctx, cfg, mgr); err != nil {
		return err
	}
	if len(mgr.List()) == 0 {
		return errors.New("no upstream configured: pass --upstream <url> or add servers via `scry servers add`")
	}

	srv := mcp.NewServer(mcp.ServerInfo{
		Name:    "scry",
		Version: "0.0.0-dev",
	}, mcp.WithInstructions(
		"scry exposes one or more GraphQL endpoints as searchable MCP tools. "+
			"Call list_servers first to see which upstreams are available. "+
			"Use schema_search to discover types before composing a query, "+
			"and query_cost to gate expensive queries before query_execute.",
	))

	if err := registerSchemaTools(srv, cfg, mgr); err != nil {
		return fmt.Errorf("register schema tools: %w", err)
	}
	if err := registerAuthTools(srv); err != nil {
		return fmt.Errorf("register auth tools: %w", err)
	}
	g, err := gate.New(gate.Policy{
		MaxWritesPerSession:     cfg.SessionWriteLimit,
		MaxComplexityPerSession: cfg.SessionComplexityLimit,
		EvidenceLimit:           cfg.EvidenceLimit,
		AuditDir:                cfg.AuditDir,
		AuditMaxSize:            cfg.AuditMaxSize,
		AuditKeep:               cfg.AuditKeep,
		AuditEmitter: func(session gate.SessionID, ev gate.Evidence) {
			obs.EmitAuditEvent(obs.AuditEvent{
				Timestamp:    ev.Timestamp,
				Session:      string(session),
				Server:       ev.Server,
				Effect:       string(ev.Effect),
				Outcome:      ev.Outcome,
				Complexity:   ev.Complexity,
				QueryHash:    ev.QueryHash,
				ResponseHash: ev.ResponseHash,
				ChainHash:    ev.ChainHash,
			})
		},
	})
	if err != nil {
		return fmt.Errorf("build gate: %w", err)
	}
	defer func() { _ = g.Close() }()
	if err := registerQueryTools(srv, cfg, mgr, g); err != nil {
		return fmt.Errorf("register query tools: %w", err)
	}
	if err := registerGateTools(srv, g); err != nil {
		return fmt.Errorf("register gate tools: %w", err)
	}
	if err := registerRuntimeTools(srv, mgr); err != nil {
		return fmt.Errorf("register runtime tools: %w", err)
	}
	if err := registerCacheTools(srv, mgr); err != nil {
		return fmt.Errorf("register cache tools: %w", err)
	}
	if err := registerWebhookTools(srv, mgr); err != nil {
		return fmt.Errorf("register webhook tools: %w", err)
	}

	if cfg.RefreshInterval > 0 {
		go runRefresher(ctx, cfg, mgr)
	}

	// Hot reload only makes sense in multi-upstream mode — the
	// single-upstream path doesn't read servers.yml. Skip when
	// --upstream was passed to keep stdio subprocess invocations
	// from spending an fsnotify watcher on a file they don't use.
	if cfg.UpstreamURL == "" {
		path, err := auth.DefaultPath()
		if err == nil {
			go func() {
				if err := runtime.WatchServers(ctx, path, mgr); err != nil {
					obs.L.Warn().
						Str("event", "watcher.exited_with_error").
						Err(err).
						Msg("servers.yml watcher exited")
				}
			}()
		}
	}

	return serveTransport(ctx, srv, cfg, mgr)
}

// seedManager populates the Manager from the configured discovery
// source. --upstream wins when set (single-server mode). Otherwise
// every entry in servers.yml is registered.
func seedManager(ctx context.Context, cfg Config, mgr *runtime.Manager) error {
	if cfg.UpstreamURL != "" || cfg.SDLFile != "" {
		// Single-upstream mode allows --sdl-file without
		// --upstream (the SDL alone is enough to serve read-only
		// tools; query_execute will surface a missing-endpoint
		// error if the operator tries to run a query).
		return mgr.Add(ctx, runtime.AddConfig{
			Name:     "default",
			Upstream: cfg.UpstreamURL,
			AuthRef:  cfg.AuthToken,
			SDLPath:  cfg.SDLFile,
		})
	}
	path, err := auth.DefaultPath()
	if err != nil {
		return fmt.Errorf("resolve servers.yml path: %w", err)
	}
	s, err := auth.Load(path)
	if err != nil {
		return fmt.Errorf("load servers.yml: %w", err)
	}
	if len(s.Servers) == 0 {
		return nil
	}
	return mgr.LoadFromServers(ctx, s)
}

// runRefresher periodically re-introspects every registered
// upstream. Per-server failures are logged but never propagated —
// transient outages must not kill scry.
func runRefresher(ctx context.Context, cfg Config, mgr *runtime.Manager) {
	t := time.NewTicker(cfg.RefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, name := range mgr.List() {
				if err := mgr.Refresh(ctx, name); err != nil {
					obs.L.Error().
						Str("event", "refresher.tick_failed").
						Str("server", name).
						Err(err).
						Msg("background introspection refresh failed")
				}
			}
		}
	}
}

// resolveIndexDir returns the directory each upstream's index file
// lives under. Honours --index, then $XDG_DATA_HOME, falling back to
// ~/.local/share/scry. The chosen dir is created on first call.
func resolveIndexDir(cfg Config) (string, error) {
	if cfg.IndexDir != "" {
		return cfg.IndexDir, nil
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "scry"), nil
}

// serveTransport picks the right mcp-go entrypoint based on
// cfg.Transport. stdio is the default. http/grpc/ws bind a port and
// expect cfg.ServeAuthToken to gate remote callers.
//
// TLS is out of scope: operators run a reverse proxy in front of
// scry. (mcp-go issue #86 tracks embedded TLS as a follow-up.)
func serveTransport(ctx context.Context, srv *mcp.Server, cfg Config, mgr *runtime.Manager) error {
	switch cfg.Transport {
	case "", "stdio":
		if cfg.ServeAuthToken != "" {
			obs.L.Warn().
				Str("event", "boot.serve_auth_ignored").
				Msg("--serve-auth is ignored on stdio transport (no remote callers)")
		}
		obs.L.Info().
			Str("event", "boot").
			Str("transport", "stdio").
			Int("servers", len(mgr.List())).
			Strs("server_names", mgr.List()).
			Msg("scry serving on stdio")
		return mcp.ServeStdio(ctx, srv)
	case "http":
		return serveHTTP(ctx, srv, cfg, mgr)
	case "grpc":
		return serveGRPC(ctx, srv, cfg, mgr)
	case "ws", "websocket":
		return serveWebSocket(ctx, srv, cfg, mgr)
	default:
		return fmt.Errorf("unsupported transport %q (want stdio | http | grpc | ws)", cfg.Transport)
	}
}

// Identity labels assigned to each configured bearer token. Used by
// the per-tool authz guard (requireAdmin) to decide whether the
// caller may invoke a destructive tool. The labels appear in
// structured logs so operators can audit which client did what.
//
// clients.yml-defined clients get their own Identity.Name (the
// client's friendly name); they are NOT identityAdmin or
// identityReadOnly, so requireAdmin denies them by default. Per-tool
// scope from clients.yml drives the actual decision via
// scopeFor(ctx).
const (
	identityAdmin    = "scry-admin"
	identityReadOnly = "scry-readonly"
)

// scopeRegistry maps the resolved transport token → its richer
// clients.yml scope. Populated at boot in buildServeOpts; nil when
// no clients.yml is loaded (in which case scopeFor returns nil and
// the legacy admin/read-only logic stands).
var scopeRegistry map[string]*auth.Scope

// scopeFor returns the clients.yml-derived scope for the active
// caller, or nil when the caller isn't in clients.yml (legacy
// --serve-auth caller, stdio, or unauthenticated). Lookup is by
// Identity.ID, which identityContextFn sets to the resolved token for
// clients.yml callers — the same key scopeRegistry is built under.
func scopeFor(ctx context.Context) *auth.Scope {
	if scopeRegistry == nil {
		return nil
	}
	id := identityFromContext(ctx)
	if id == nil {
		return nil
	}
	return scopeRegistry[id.ID]
}

// buildServeOpts assembles the shared middleware stack and the
// token→identity map every transport needs. Both --serve-auth (admin)
// and --serve-auth-readonly (read-only) are accepted concurrently;
// callers presenting the admin token get the identityAdmin identity,
// callers presenting the read-only token get identityReadOnly.
//
// mcp-go v1.19 removed its in-library BearerAuth; scry now owns bearer
// authentication. buildServeOpts returns the resolved token→identity
// map so the HTTP transport can derive identity from the Authorization
// header via identityContextFn (transport.WithRequestContextFn), and
// installs bearerGate — scry's replacement for BearerAuth's
// authentication step — plus the OTel span middleware and the
// tool-list scope filter.
//
// The returned map is nil/empty when no serve credential is
// configured, in which case bearerGate is omitted and every caller is
// treated as a trusted local operator.
func buildServeOpts(cfg Config) (map[string]*Identity, []mcp.ServeOption, error) {
	var opts []mcp.ServeOption
	identities := map[string]*Identity{}
	scopeRegistry = nil // reset on each boot (test isolation)

	// clients.yml takes precedence — adds tokens with their
	// friendly names + per-token scopes. Identity.ID is the resolved
	// token so scopeFor can look the caller's scope up in
	// scopeRegistry (keyed by the same token).
	clientsPath, err := auth.DefaultClientsPath()
	if err == nil {
		clients, lerr := auth.LoadClients(clientsPath)
		if lerr != nil {
			return nil, nil, fmt.Errorf("load clients.yml: %w", lerr)
		}
		if len(clients.Clients) > 0 {
			if err := clients.Validate(); err != nil {
				return nil, nil, fmt.Errorf("clients.yml: %w", err)
			}
			scopeRegistry = map[string]*auth.Scope{}
			for _, cl := range clients.Clients {
				tok, err := auth.ResolveToken(cl.Token)
				if err != nil {
					return nil, nil, fmt.Errorf("clients.yml %q: %w", cl.Name, err)
				}
				if _, dup := identities[tok]; dup {
					return nil, nil, fmt.Errorf("clients.yml %q: token collides with another registered token", cl.Name)
				}
				identities[tok] = &Identity{ID: tok, Name: cl.Name}
				scope, err := cl.BuildScope(nil)
				if err != nil {
					return nil, nil, fmt.Errorf("clients.yml: %w", err)
				}
				scopeRegistry[tok] = &scope
			}
			obs.L.Info().
				Str("event", "boot.clients_loaded").
				Int("clients", len(clients.Clients)).
				Strs("client_names", clients.Names()).
				Msg("loaded per-client scopes from clients.yml")
		}
	}

	if cfg.ServeAuthToken != "" {
		tok, err := auth.ResolveToken(cfg.ServeAuthToken)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve serve-auth: %w", err)
		}
		if _, dup := identities[tok]; dup {
			return nil, nil, errors.New("--serve-auth resolved to a token already used by clients.yml")
		}
		// --serve-auth callers carry no clients.yml scope; ID == Name
		// == identityAdmin so requireAdmin allows them.
		identities[tok] = &Identity{ID: identityAdmin, Name: identityAdmin}
	}
	if cfg.ServeAuthTokenReadOnly != "" {
		tok, err := auth.ResolveToken(cfg.ServeAuthTokenReadOnly)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve serve-auth-readonly: %w", err)
		}
		// Refuse to silently overwrite an admin token entry if
		// operators paste the same value in both flags — without
		// this check, the higher privilege would silently win and
		// the read-only label would be dropped.
		if _, dup := identities[tok]; dup {
			return nil, nil, errors.New("--serve-auth and --serve-auth-readonly resolved to the same token; use distinct credentials")
		}
		identities[tok] = &Identity{ID: identityReadOnly, Name: identityReadOnly}
	}

	// OTel middleware wraps every MCP request in a span. Cheap when
	// the no-op provider is active (no traces exported); valuable
	// when operators have OTEL_TRACES_EXPORTER set. Skip the
	// handshake methods so we don't generate spans for every
	// initialize/ping.
	otelMW := mcp.OTel(
		mcp.WithOTelServiceName("scry"),
		mcp.WithOTelSkipMethods("initialize", "notifications/initialized", "ping"),
	)
	opts = append(opts, mcp.WithMiddleware(otelMW))

	if len(identities) == 0 {
		// Even without auth, install the tool-list filter so a
		// clients.yml-only configuration (rare) is gated. Cheap
		// no-op when no scope is registered.
		opts = append(opts, mcp.WithMiddleware(toolListFilter()))
		return identities, opts, nil
	}
	opts = append(opts, mcp.WithMiddleware(bearerGate()))
	opts = append(opts, mcp.WithMiddleware(toolListFilter()))
	return identities, opts, nil
}

// buildTLSConfig assembles a *tls.Config from the cert/key/CA
// flags. Returns nil when --tls-cert is empty (TLS disabled).
// Validates that flags are consistent: cert without key is an
// error; CA without cert is an error. Operators get a clear
// message at boot rather than a confusing runtime failure.
func buildTLSConfig(cfg Config) (*tls.Config, error) {
	if cfg.TLSCertFile == "" {
		if cfg.TLSKeyFile != "" || cfg.MTLSCAFile != "" {
			return nil, errors.New("--tls-key / --mtls-ca require --tls-cert")
		}
		return nil, nil
	}
	if cfg.TLSKeyFile == "" {
		return nil, errors.New("--tls-cert requires --tls-key")
	}
	cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS cert/key: %w", err)
	}
	t := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	if cfg.MTLSCAFile != "" {
		caPEM, err := os.ReadFile(cfg.MTLSCAFile)
		if err != nil {
			return nil, fmt.Errorf("read mTLS CA bundle: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, errors.New("mTLS CA bundle had no usable certificates")
		}
		t.ClientCAs = pool
		t.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return t, nil
}

func serveHTTP(ctx context.Context, srv *mcp.Server, cfg Config, mgr *runtime.Manager) error {
	identities, serveOpts, err := buildServeOpts(cfg)
	if err != nil {
		return err
	}
	tlsCfg, err := buildTLSConfig(cfg)
	if err != nil {
		return err
	}
	httpOpts := []mcp.HTTPOption{
		mcp.WithReadTimeout(30 * time.Second),
		mcp.WithWriteTimeout(30 * time.Second),
	}
	if len(identities) > 0 {
		// Derive caller identity from the Authorization header at the
		// transport layer — the only place the header is reachable
		// after mcp-go v1.19 removed in-library auth. bearerGate (in
		// serveOpts) enforces it.
		httpOpts = append(httpOpts, transport.WithRequestContextFn(identityContextFn(identities)))
	}
	if tlsCfg != nil {
		httpOpts = append(httpOpts, mcp.WithTLSConfig(tlsCfg))
	}
	logBoot("http", cfg, mgr)
	return mcp.ServeHTTPWithMiddleware(ctx, srv, cfg.ListenAddr, httpOpts, serveOpts...)
}

func serveGRPC(ctx context.Context, srv *mcp.Server, cfg Config, mgr *runtime.Manager) error {
	identities, serveOpts, err := buildServeOpts(cfg)
	if err != nil {
		return err
	}
	warnBearerUnsupported("grpc", identities)
	tlsCfg, err := buildTLSConfig(cfg)
	if err != nil {
		return err
	}
	grpcOpts := []mcp.GRPCOption{
		mcp.WithGRPCShutdownTimeout(10 * time.Second),
	}
	if tlsCfg != nil {
		grpcOpts = append(grpcOpts, mcp.WithGRPCTLSConfig(tlsCfg))
	}
	logBoot("grpc", cfg, mgr)
	return mcp.ServeGRPCWithMiddleware(ctx, srv, cfg.ListenAddr, grpcOpts, serveOpts...)
}

func serveWebSocket(ctx context.Context, srv *mcp.Server, cfg Config, mgr *runtime.Manager) error {
	identities, serveOpts, err := buildServeOpts(cfg)
	if err != nil {
		return err
	}
	warnBearerUnsupported("ws", identities)
	tlsCfg, err := buildTLSConfig(cfg)
	if err != nil {
		return err
	}
	var wsOpts []mcp.WebSocketOption
	if tlsCfg != nil {
		wsOpts = append(wsOpts, mcp.WithWebSocketTLSConfig(tlsCfg))
	}
	logBoot("ws", cfg, mgr)
	return mcp.ServeWebSocketWithMiddleware(ctx, srv, cfg.ListenAddr, wsOpts, serveOpts...)
}

// warnBearerUnsupported flags the one configuration mcp-go v1.19's
// auth removal left unservable: bearer tokens over grpc/ws. Only the
// HTTP transport exposes a per-request hook (WithRequestContextFn) to
// read the Authorization header, so identityContextFn can't run on
// grpc/ws. bearerGate then sees a nil identity for every non-handshake
// call and rejects it (fail-closed, never fail-open) — secure, but the
// transport can't authenticate anyone. Operators needing bearer auth
// must use --transport http.
func warnBearerUnsupported(transport string, identities map[string]*Identity) {
	if len(identities) == 0 {
		return
	}
	obs.L.Warn().
		Str("event", "boot.bearer_auth_unsupported").
		Str("transport", transport).
		Msg("serve-auth / clients.yml bearer tokens cannot be derived on grpc/ws (no per-request header hook after mcp-go v1.19); authenticated calls will be rejected — use --transport http")
}

// logBoot emits the structured boot event every transport shares.
func logBoot(transport string, cfg Config, mgr *runtime.Manager) {
	obs.L.Info().
		Str("event", "boot").
		Str("transport", transport).
		Str("listen", cfg.ListenAddr).
		Int("servers", len(mgr.List())).
		Strs("server_names", mgr.List()).
		Bool("auth_enabled", cfg.ServeAuthToken != "" || cfg.ServeAuthTokenReadOnly != "").
		Bool("readonly_token_configured", cfg.ServeAuthTokenReadOnly != "").
		Str("serve_auth_ref", obs.RedactTokenRef(cfg.ServeAuthToken)).
		Str("serve_auth_readonly_ref", obs.RedactTokenRef(cfg.ServeAuthTokenReadOnly)).
		Bool("tls_enabled", cfg.TLSCertFile != "").
		Bool("mtls_enabled", cfg.MTLSCAFile != "").
		Msg("scry transport listening")
}

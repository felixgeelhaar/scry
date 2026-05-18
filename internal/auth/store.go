// Package auth manages scry's credential store at
// `$XDG_CONFIG_HOME/scry/servers.yml`. The file is plain YAML
// owned by the user (mode 0600); scry refuses to load it when the
// permissions are looser than that.
//
// Phase 1 (this revision) supports bearer-token auth only. Device
// code, PKCE, and OS-keychain backends are listed in
// docs/auth-design.md and ship later.
package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Servers is the YAML root. The `version` field gives us a hook for
// future schema migrations without breaking existing files.
type Servers struct {
	Version int               `yaml:"version"`
	Servers map[string]Server `yaml:"servers"`
}

// Server is one entry — an upstream URL + the auth needed to reach it.
type Server struct {
	Upstream string `yaml:"upstream"`
	Auth     Auth   `yaml:"auth"`
	// SDLPath, when non-empty, points to a local SDL file scry
	// loads instead of running introspection against the upstream.
	// Right for upstreams that disable introspection or sit behind
	// a CDN that rejects the standard query at any depth.
	SDLPath string `yaml:"sdl_path,omitempty"`
	// RateLimit caps the requests scry sends to this upstream.
	// Token bucket: Rate refilled per second; Burst is the bucket
	// size. Zero values disable the gate.
	RateLimit RateLimit `yaml:"rate_limit,omitempty"`
}

// RateLimit configures a per-server token-bucket gate. Zero values
// disable. Burst defaults to Rate when omitted so operators can write
// `rate_limit: { rps: 5 }` and get sane bursting behaviour without
// thinking about it.
type RateLimit struct {
	RPS   float64 `yaml:"rps,omitempty"`
	Burst int     `yaml:"burst,omitempty"`
}

// Auth is the credential block. Token is the only payload v0 cares
// about; ExpiresAt drives the auth_status traffic-light output.
//
// Future extensions (refresh_token, client_id, scope) can be added
// without breaking existing files because yaml.v3 ignores unknown
// fields by default.
type Auth struct {
	Type      string    `yaml:"type"`
	Token     string    `yaml:"token,omitempty"`
	ExpiresAt time.Time `yaml:"expires_at,omitempty"`
	RefreshAt time.Time `yaml:"refresh_at,omitempty"`
	// HeaderName overrides the HTTP header scry writes the
	// credential into. Default "Authorization" — set this when the
	// upstream uses a non-standard header (e.g. "X-API-Key").
	HeaderName string `yaml:"header_name,omitempty"`
	// Scheme overrides the value prefix. Default "Bearer". Set to
	// "" (an explicit empty string in YAML) when the upstream
	// expects the raw credential with no prefix.
	Scheme *string `yaml:"scheme,omitempty"`
}

// HeaderAndScheme returns the resolved header name + value scheme for
// this Auth block. Centralised so callers get the same defaults
// without re-implementing the precedence rules.
//
// Returns (headerName, scheme, hasScheme). hasScheme=false signals an
// empty prefix (raw credential value).
func (a Auth) HeaderAndScheme() (string, string, bool) {
	header := a.HeaderName
	if header == "" {
		header = "Authorization"
	}
	if a.Scheme == nil {
		return header, "Bearer", true
	}
	if *a.Scheme == "" {
		return header, "", false
	}
	return header, *a.Scheme, true
}

// Status is the v0 traffic light for one server's credentials.
type Status string

const (
	StatusValid    Status = "valid"
	StatusExpiring Status = "expiring"
	StatusExpired  Status = "expired"
	StatusMissing  Status = "missing"
)

// expiringWindow is how far in advance of ExpiresAt we report
// "expiring" instead of "valid". Tuned so agents have time to call
// auth_login before a query fails mid-task.
const expiringWindow = 10 * time.Minute

// DefaultPath returns the canonical servers.yml location. Honours
// XDG_CONFIG_HOME so test setups can isolate without touching the
// user's real config.
func DefaultPath() (string, error) {
	if p := os.Getenv("SCRY_SERVERS_PATH"); p != "" {
		return p, nil
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		cfg = filepath.Join(home, ".config")
	}
	return filepath.Join(cfg, "scry", "servers.yml"), nil
}

// ErrInsecurePerms is returned when servers.yml is readable beyond
// the owner. Loading refuses to proceed so a misconfigured file
// (e.g. checked in to git accidentally) can't leak tokens further.
var ErrInsecurePerms = errors.New("servers.yml has world/group-readable permissions; chmod 600 to fix")

// Load reads servers.yml from path. Returns an empty (but non-nil)
// Servers with Version=1 when the file doesn't exist — first-run
// callers don't need to handle os.IsNotExist themselves.
//
// Enforces mode 0600 on POSIX. Windows has different ACL semantics
// so the perm check is skipped there.
func Load(path string) (*Servers, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Servers{Version: 1, Servers: map[string]Server{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stat servers.yml: %w", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w (current mode: %o)", ErrInsecurePerms, info.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read servers.yml: %w", err)
	}
	out := &Servers{Servers: map[string]Server{}}
	if err := yaml.Unmarshal(b, out); err != nil {
		return nil, fmt.Errorf("parse servers.yml: %w", err)
	}
	if out.Servers == nil {
		out.Servers = map[string]Server{}
	}
	if out.Version == 0 {
		out.Version = 1
	}
	return out, nil
}

// Save writes servers.yml atomically with mode 0600. Writes to a
// tempfile in the same directory then renames — avoids leaving a
// truncated file if the process dies mid-write.
func Save(s *Servers, path string) error {
	if s == nil {
		return errors.New("nil Servers")
	}
	if s.Version == 0 {
		s.Version = 1
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	b, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".servers.yml.*")
	if err != nil {
		return fmt.Errorf("tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// Status returns the credential traffic light for one server.
// Missing server / missing token both surface as StatusMissing so
// callers can branch on a single signal.
func (s *Servers) Status(name string, now time.Time) Status {
	srv, ok := s.Servers[name]
	if !ok || srv.Auth.Token == "" {
		return StatusMissing
	}
	if srv.Auth.ExpiresAt.IsZero() {
		return StatusValid
	}
	if !now.Before(srv.Auth.ExpiresAt) {
		return StatusExpired
	}
	if srv.Auth.ExpiresAt.Sub(now) <= expiringWindow {
		return StatusExpiring
	}
	return StatusValid
}

// StatusEntry is one row in the auth_status response — strips
// the token (callers must never see the secret itself) and
// reports the timing fields.
type StatusEntry struct {
	Name             string `json:"name"`
	Upstream         string `json:"upstream"`
	Type             string `json:"type"`
	Status           Status `json:"status"`
	ExpiresAt        string `json:"expires_at,omitempty"`
	ExpiresInSeconds int64  `json:"expires_in_seconds,omitempty"`
}

// StatusAll returns one entry per configured server, sorted by name
// so output is stable for tests and human eyeballs.
func (s *Servers) StatusAll(now time.Time) []StatusEntry {
	names := make([]string, 0, len(s.Servers))
	for n := range s.Servers {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]StatusEntry, 0, len(names))
	for _, n := range names {
		srv := s.Servers[n]
		e := StatusEntry{
			Name:     n,
			Upstream: srv.Upstream,
			Type:     srv.Auth.Type,
			Status:   s.Status(n, now),
		}
		if !srv.Auth.ExpiresAt.IsZero() {
			e.ExpiresAt = srv.Auth.ExpiresAt.UTC().Format(time.RFC3339)
			e.ExpiresInSeconds = int64(srv.Auth.ExpiresAt.Sub(now).Seconds())
		}
		out = append(out, e)
	}
	return out
}

// Upsert sets a server entry, creating the map if needed. Returns
// true when the entry was new (false on overwrite) so the CLI can
// tell the user which path fired.
func (s *Servers) Upsert(name string, srv Server) bool {
	if s.Servers == nil {
		s.Servers = map[string]Server{}
	}
	_, existed := s.Servers[name]
	s.Servers[name] = srv
	return !existed
}

// Remove deletes a server. Returns true when an entry was removed.
func (s *Servers) Remove(name string) bool {
	if _, ok := s.Servers[name]; !ok {
		return false
	}
	delete(s.Servers, name)
	return true
}

// Validate runs lightweight checks before Save — surfaces obvious
// misconfiguration (missing upstream URL, unsupported auth type)
// instead of letting it slip into the file.
func (s *Servers) Validate() error {
	for name, srv := range s.Servers {
		if srv.Upstream == "" {
			return fmt.Errorf("server %q: upstream URL is empty", name)
		}
		if srv.Auth.Type != "" && srv.Auth.Type != "bearer" {
			return fmt.Errorf("server %q: auth.type %q not supported in v0 (only 'bearer')", name, srv.Auth.Type)
		}
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("server has empty name")
		}
	}
	return nil
}

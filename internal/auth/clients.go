package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Clients is the YAML root of `$XDG_CONFIG_HOME/scry/clients.yml` —
// scry's richer authz file. Each entry binds a bearer token (or
// token-ref) to:
//
//   - a friendly name surfaced in logs + the IdentityFromContext API
//   - the set of MCP tools the client may call (* = all)
//   - the set of upstream servers the client may target (* = all)
//
// When both --serve-auth + clients.yml exist, clients.yml wins. The
// admin/read-only --serve-auth flags stay as a quick-start path;
// clients.yml is for deployments with multiple agents of varying
// trust.
type Clients struct {
	Version int      `yaml:"version"`
	Clients []Client `yaml:"clients"`
}

// Client is one row. Token accepts the same reference schemes as
// the server auth fields (env://, file://, op://, literal).
type Client struct {
	Name    string   `yaml:"name"`
	Token   string   `yaml:"token"`
	Tools   []string `yaml:"tools"`   // ["*"] or explicit list
	Servers []string `yaml:"servers"` // ["*"] or explicit list (matched against runtime server names)
}

// DefaultClientsPath returns the canonical clients.yml location.
// SCRY_CLIENTS_PATH env var overrides for tests + alternative
// layouts.
func DefaultClientsPath() (string, error) {
	if p := os.Getenv("SCRY_CLIENTS_PATH"); p != "" {
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
	return filepath.Join(cfg, "scry", "clients.yml"), nil
}

// LoadClients reads clients.yml. Returns an empty (non-nil) Clients
// when the file is missing — single-process happy path doesn't
// need the file.
//
// Enforces 0600 perms on POSIX (same model as servers.yml). The
// file holds bearer tokens.
func LoadClients(path string) (*Clients, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Clients{Version: 1}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stat clients.yml: %w", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("clients.yml has world/group-readable permissions; chmod 600 to fix (current mode: %o)", info.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read clients.yml: %w", err)
	}
	out := &Clients{}
	if err := yaml.Unmarshal(b, out); err != nil {
		return nil, fmt.Errorf("parse clients.yml: %w", err)
	}
	if out.Version == 0 {
		out.Version = 1
	}
	return out, nil
}

// Validate runs cheap consistency checks: names are non-empty +
// unique, tokens resolve to non-empty values (via ResolveToken),
// scopes contain at least one entry. Returns the first error it
// hits so operators can fix one thing at a time.
func (c *Clients) Validate() error {
	seenName := map[string]bool{}
	seenToken := map[string]string{}
	for i, cl := range c.Clients {
		if strings.TrimSpace(cl.Name) == "" {
			return fmt.Errorf("client %d: name is empty", i)
		}
		if seenName[cl.Name] {
			return fmt.Errorf("client %q: duplicate name", cl.Name)
		}
		seenName[cl.Name] = true

		if cl.Token == "" {
			return fmt.Errorf("client %q: token is empty", cl.Name)
		}
		tok, err := ResolveToken(cl.Token)
		if err != nil {
			return fmt.Errorf("client %q: %w", cl.Name, err)
		}
		if tok == "" {
			return fmt.Errorf("client %q: token resolves to an empty string", cl.Name)
		}
		if other, dup := seenToken[tok]; dup {
			return fmt.Errorf("client %q: token collides with client %q (each client must have a distinct credential)", cl.Name, other)
		}
		seenToken[tok] = cl.Name

		if len(cl.Tools) == 0 {
			return fmt.Errorf("client %q: tools list is empty (use [\"*\"] for all)", cl.Name)
		}
		if len(cl.Servers) == 0 {
			return fmt.Errorf("client %q: servers list is empty (use [\"*\"] for all)", cl.Name)
		}
	}
	return nil
}

// Scope is one client's resolved permission set, returned to the
// scry runtime layer. Wildcards are pre-expanded against the
// runtime's known server names so handler-time checks are O(1)
// map lookups.
type Scope struct {
	Name           string
	AllowAllTools  bool
	AllowedTools   map[string]bool
	AllowAllServers bool
	AllowedServers map[string]bool
}

// BuildScope returns the resolved Scope for one client. knownServers
// is the runtime.Manager's current server-name list — wildcards
// expand against it so a "*" in clients.yml automatically picks up
// servers added later via hot reload.
func (c Client) BuildScope(knownServers []string) Scope {
	s := Scope{Name: c.Name}
	for _, t := range c.Tools {
		if t == "*" {
			s.AllowAllTools = true
			break
		}
	}
	if !s.AllowAllTools {
		s.AllowedTools = map[string]bool{}
		for _, t := range c.Tools {
			s.AllowedTools[t] = true
		}
	}
	for _, srv := range c.Servers {
		if srv == "*" {
			s.AllowAllServers = true
			break
		}
	}
	if !s.AllowAllServers {
		s.AllowedServers = map[string]bool{}
		for _, srv := range c.Servers {
			s.AllowedServers[srv] = true
		}
	}
	_ = knownServers // reserved for future wildcard-with-deny patterns
	return s
}

// MayCallTool reports whether this scope permits the named MCP tool.
func (s *Scope) MayCallTool(tool string) bool {
	if s == nil {
		// nil scope = no clients.yml in play. Falls back to the
		// --serve-auth admin/read-only logic in authz.go.
		return true
	}
	return s.AllowAllTools || s.AllowedTools[tool]
}

// MayCallServer reports whether this scope permits the named
// upstream. Empty server name (single-upstream default routing) is
// always allowed — the scope only kicks in when the caller picks
// an explicit target.
func (s *Scope) MayCallServer(server string) bool {
	if s == nil {
		return true
	}
	if server == "" {
		return true
	}
	return s.AllowAllServers || s.AllowedServers[server]
}

// Names returns client names sorted alphabetically — used by status
// + audit tooling.
func (c *Clients) Names() []string {
	out := make([]string, 0, len(c.Clients))
	for _, cl := range c.Clients {
		out = append(out, cl.Name)
	}
	sort.Strings(out)
	return out
}

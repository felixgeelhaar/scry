package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// TenantsDir returns the directory holding per-tenant server-list
// overlays: $XDG_CONFIG_HOME/scry/tenants. SCRY_TENANTS_DIR env
// var overrides for tests.
func TenantsDir() (string, error) {
	if d := os.Getenv("SCRY_TENANTS_DIR"); d != "" {
		return d, nil
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		cfg = filepath.Join(home, ".config")
	}
	return filepath.Join(cfg, "scry", "tenants"), nil
}

// LoadTenant returns the per-tenant server overlay, or nil + nil
// error when the file is absent. Overlay shape mirrors the base
// servers.yml: keys merged into the base map by Name, per-tenant
// entries taking precedence (full replacement of the entry; no
// per-field merging).
//
// Tenant names are validated against a conservative allowlist —
// alphanumeric, dash, underscore. Anything else returns an error
// rather than risking path traversal via "../" in the filename.
func LoadTenant(tenant string) (*Servers, error) {
	if !validTenantName(tenant) {
		return nil, fmt.Errorf("invalid tenant name %q (allowed: [a-zA-Z0-9_-]+)", tenant)
	}
	dir, err := TenantsDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, tenant+".yml")
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stat tenant %s: %w", tenant, err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("tenant %s overlay has world/group-readable permissions; chmod 600", tenant)
	}
	return Load(path)
}

// MergeTenant overlays per-tenant entries on top of base. Returns a
// new *Servers — neither input is mutated. Per-tenant Server fully
// replaces any same-named entry in the base.
func MergeTenant(base, overlay *Servers) *Servers {
	out := &Servers{Servers: map[string]Server{}}
	if base != nil {
		for k, v := range base.Servers {
			out.Servers[k] = v
		}
	}
	if overlay != nil {
		for k, v := range overlay.Servers {
			out.Servers[k] = v
		}
	}
	return out
}

func validTenantName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

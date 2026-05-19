package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidTenantName(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"acme", true},
		{"acme_corp", true},
		{"acme-corp", true},
		{"AcmeCorp1", true},
		{"", false},
		{"../escape", false},
		{"with space", false},
		{"slash/in", false},
		{"dot.in", false},
	} {
		if got := validTenantName(c.in); got != c.want {
			t.Errorf("validTenantName(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestLoadTenantMissingFileReturnsNil(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SCRY_TENANTS_DIR", dir)
	got, err := LoadTenant("acme")
	if err != nil {
		t.Fatalf("LoadTenant: %v", err)
	}
	if got != nil {
		t.Errorf("missing tenant must return nil, got %+v", got)
	}
}

func TestLoadTenantRejectsBadName(t *testing.T) {
	if _, err := LoadTenant("../escape"); err == nil {
		t.Errorf("path-traversal name must be rejected")
	}
}

func TestLoadTenantReadsOverlay(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SCRY_TENANTS_DIR", dir)
	path := filepath.Join(dir, "acme.yml")
	if err := os.WriteFile(path, []byte(`
servers:
  acme-graphql:
    upstream: https://api.acme.example.com/graphql
    auth: { type: bearer, token: env://ACME_TOKEN }
`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := LoadTenant("acme")
	if err != nil {
		t.Fatalf("LoadTenant: %v", err)
	}
	if got == nil || got.Servers["acme-graphql"].Upstream == "" {
		t.Errorf("overlay not parsed: %+v", got)
	}
}

func TestLoadTenantRejectsInsecurePerms(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SCRY_TENANTS_DIR", dir)
	path := filepath.Join(dir, "acme.yml")
	if err := os.WriteFile(path, []byte(`servers: {}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadTenant("acme"); err == nil {
		t.Errorf("world-readable tenant file must be rejected")
	}
}

func TestMergeTenantOverlayWinsOnConflict(t *testing.T) {
	base := &Servers{Servers: map[string]Server{
		"shared":    {Upstream: "https://base.example.com/graphql"},
		"only-base": {Upstream: "https://base-only.example.com/graphql"},
	}}
	overlay := &Servers{Servers: map[string]Server{
		"shared":       {Upstream: "https://overlay.example.com/graphql"},
		"only-overlay": {Upstream: "https://overlay-only.example.com/graphql"},
	}}
	merged := MergeTenant(base, overlay)
	if merged.Servers["shared"].Upstream != "https://overlay.example.com/graphql" {
		t.Errorf("overlay should win on conflict; got %+v", merged.Servers["shared"])
	}
	if _, ok := merged.Servers["only-base"]; !ok {
		t.Errorf("base-only entries should be retained")
	}
	if _, ok := merged.Servers["only-overlay"]; !ok {
		t.Errorf("overlay-only entries should be present")
	}
}

func TestMergeTenantNilSafe(t *testing.T) {
	if MergeTenant(nil, nil).Servers == nil {
		t.Errorf("nil+nil must return non-nil empty Servers")
	}
}

func TestScopeTenantOfDefaults(t *testing.T) {
	var nilScope *Scope
	if got := nilScope.TenantOf(); got != DefaultTenant {
		t.Errorf("nil scope must default to %q, got %q", DefaultTenant, got)
	}
	s := &Scope{Tenant: ""}
	if got := s.TenantOf(); got != DefaultTenant {
		t.Errorf("empty Tenant must default to %q, got %q", DefaultTenant, got)
	}
	s.Tenant = "acme"
	if got := s.TenantOf(); got != "acme" {
		t.Errorf("explicit Tenant should pass through, got %q", got)
	}
}

package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadClientsMissingReturnsEmpty(t *testing.T) {
	c, err := LoadClients(filepath.Join(t.TempDir(), "clients.yml"))
	if err != nil {
		t.Fatalf("load missing: %v", err)
	}
	if len(c.Clients) != 0 {
		t.Errorf("expected empty, got %+v", c)
	}
}

func TestLoadClientsRefusesInsecurePerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("perm check skipped on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "clients.yml")
	if err := os.WriteFile(path, []byte("version: 1\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadClients(path)
	if err == nil || !strings.Contains(err.Error(), "world/group-readable") {
		t.Errorf("expected perm error, got %v", err)
	}
}

func TestClientsValidateChecksUniquenessAndScopes(t *testing.T) {
	cases := []struct {
		name      string
		clients   []Client
		wantError string
	}{
		{
			name: "duplicate name",
			clients: []Client{
				{Name: "a", Token: "t1", Tools: []string{"*"}, Servers: []string{"*"}},
				{Name: "a", Token: "t2", Tools: []string{"*"}, Servers: []string{"*"}},
			},
			wantError: "duplicate name",
		},
		{
			name: "duplicate token",
			clients: []Client{
				{Name: "a", Token: "same", Tools: []string{"*"}, Servers: []string{"*"}},
				{Name: "b", Token: "same", Tools: []string{"*"}, Servers: []string{"*"}},
			},
			wantError: "collides",
		},
		{
			name: "empty tools",
			clients: []Client{
				{Name: "a", Token: "t1", Tools: nil, Servers: []string{"*"}},
			},
			wantError: "tools list is empty",
		},
		{
			name: "empty servers",
			clients: []Client{
				{Name: "a", Token: "t1", Tools: []string{"*"}, Servers: nil},
			},
			wantError: "servers list is empty",
		},
		{
			name: "empty token",
			clients: []Client{
				{Name: "a", Token: "", Tools: []string{"*"}, Servers: []string{"*"}},
			},
			wantError: "token is empty",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := (&Clients{Clients: c.clients}).Validate()
			if err == nil || !strings.Contains(err.Error(), c.wantError) {
				t.Errorf("want %q in error, got %v", c.wantError, err)
			}
		})
	}
}

func TestBuildScopeExpandsWildcards(t *testing.T) {
	full := Client{Name: "a", Token: "t", Tools: []string{"*"}, Servers: []string{"*"}}.BuildScope(nil)
	if !full.AllowAllTools || !full.AllowAllServers {
		t.Errorf("wildcards should grant all, got %+v", full)
	}

	scoped := Client{
		Name: "b", Token: "t",
		Tools:   []string{"schema_search", "schema_get"},
		Servers: []string{"shopify"},
	}.BuildScope(nil)
	if scoped.AllowAllTools || scoped.AllowAllServers {
		t.Errorf("explicit lists should not be wildcard, got %+v", scoped)
	}
	if !scoped.MayCallTool("schema_search") {
		t.Errorf("schema_search should be allowed")
	}
	if scoped.MayCallTool("query_execute") {
		t.Errorf("query_execute should be denied")
	}
	if !scoped.MayCallServer("shopify") {
		t.Errorf("shopify should be allowed")
	}
	if scoped.MayCallServer("linear") {
		t.Errorf("linear should be denied")
	}
}

func TestNilScopeAllowsEverything(t *testing.T) {
	var s *Scope
	if !s.MayCallTool("anything") || !s.MayCallServer("anything") {
		t.Errorf("nil scope should be permissive (fallback to --serve-auth logic)")
	}
}

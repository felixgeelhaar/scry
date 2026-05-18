package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLoadMissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	s, err := Load(filepath.Join(dir, "servers.yml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s == nil || len(s.Servers) != 0 {
		t.Errorf("expected empty servers, got %+v", s)
	}
	if s.Version != 1 {
		t.Errorf("expected version 1, got %d", s.Version)
	}
}

func TestSaveAndLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.yml")
	in := &Servers{Servers: map[string]Server{
		"shopify": {
			Upstream: "https://api.shopify.com/admin/api/2024-01/graphql.json",
			Auth:     Auth{Type: "bearer", Token: "shpat_xxx", ExpiresAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)},
		},
	}}
	if err := Save(in, path); err != nil {
		t.Fatalf("save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o, want 0600", info.Mode().Perm())
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := out.Servers["shopify"].Auth.Token; got != "shpat_xxx" {
		t.Errorf("token roundtrip lost: got %q", got)
	}
}

func TestLoadRefusesInsecurePerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("perm check skipped on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.yml")
	if err := os.WriteFile(path, []byte("version: 1\nservers: {}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "world/group-readable") {
		t.Errorf("expected ErrInsecurePerms, got %v", err)
	}
}

func TestStatusTrafficLight(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	s := &Servers{Servers: map[string]Server{
		"valid":    {Auth: Auth{Type: "bearer", Token: "t1", ExpiresAt: now.Add(24 * time.Hour)}},
		"expiring": {Auth: Auth{Type: "bearer", Token: "t2", ExpiresAt: now.Add(5 * time.Minute)}},
		"expired":  {Auth: Auth{Type: "bearer", Token: "t3", ExpiresAt: now.Add(-1 * time.Hour)}},
		"missing":  {Auth: Auth{Type: "bearer"}},
		"noexpiry": {Auth: Auth{Type: "bearer", Token: "t4"}},
	}}
	cases := map[string]Status{
		"valid":    StatusValid,
		"expiring": StatusExpiring,
		"expired":  StatusExpired,
		"missing":  StatusMissing,
		"noexpiry": StatusValid,
	}
	for name, want := range cases {
		if got := s.Status(name, now); got != want {
			t.Errorf("Status(%s) = %s, want %s", name, got, want)
		}
	}
	if got := s.Status("doesnotexist", now); got != StatusMissing {
		t.Errorf("unknown server should be StatusMissing, got %s", got)
	}
}

func TestStatusAllStripsTokens(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	s := &Servers{Servers: map[string]Server{
		"shopify": {Upstream: "https://shopify", Auth: Auth{Type: "bearer", Token: "secret-token", ExpiresAt: now.Add(time.Hour)}},
	}}
	entries := s.StatusAll(now)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	// Walk the rendered struct via Go's encoding/json analog —
	// formatted output must never embed the secret.
	for _, e := range entries {
		raw := e.Name + e.Upstream + e.Type + string(e.Status) + e.ExpiresAt
		if strings.Contains(raw, "secret-token") {
			t.Errorf("status entry leaked token: %+v", e)
		}
	}
}

func TestUpsertRemoveValidate(t *testing.T) {
	s := &Servers{}
	isNew := s.Upsert("a", Server{Upstream: "https://a", Auth: Auth{Type: "bearer", Token: "x"}})
	if !isNew {
		t.Errorf("first upsert should report new=true")
	}
	if isNew := s.Upsert("a", Server{Upstream: "https://a2", Auth: Auth{Type: "bearer", Token: "y"}}); isNew {
		t.Errorf("overwrite should report new=false")
	}
	if err := s.Validate(); err != nil {
		t.Errorf("validate clean state: %v", err)
	}
	s.Upsert("bad", Server{Upstream: "", Auth: Auth{Type: "bearer"}})
	if err := s.Validate(); err == nil {
		t.Errorf("expected validation error for empty upstream")
	}
	s.Remove("bad")
	if err := s.Validate(); err != nil {
		t.Errorf("after remove: %v", err)
	}
	s.Upsert("badtype", Server{Upstream: "https://x", Auth: Auth{Type: "oauth-pkce"}})
	if err := s.Validate(); err == nil {
		t.Errorf("expected validation error for unsupported auth type")
	}
}

func TestDefaultPathHonoursOverride(t *testing.T) {
	t.Setenv("SCRY_SERVERS_PATH", "/tmp/scry-test/servers.yml")
	p, err := DefaultPath()
	if err != nil {
		t.Fatalf("default path: %v", err)
	}
	if p != "/tmp/scry-test/servers.yml" {
		t.Errorf("override ignored, got %q", p)
	}
}

package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/felixgeelhaar/scry/internal/auth"
)

// TestReplaceAddsRemovesAndRotates covers the three diff paths
// Replace handles minimally: add a brand-new server, remove a gone
// server, rotate a token without touching the cached index.
func TestReplaceAddsRemovesAndRotates(t *testing.T) {
	ctx := context.Background()
	mgr, _ := New(t.TempDir(), 0)
	defer func() { _ = mgr.Close() }()

	upA := fakeUpstream(t, "alpha")
	upB := fakeUpstream(t, "beta")

	// Initial state: server "a" with a literal token.
	initial := &auth.Servers{Servers: map[string]auth.Server{
		"a": {Upstream: upA, Auth: auth.Auth{Type: "bearer", Token: "tok-1"}},
	}}
	if err := mgr.Replace(ctx, initial); err != nil {
		t.Fatalf("initial replace: %v", err)
	}
	if got := mgr.List(); len(got) != 1 || got[0] != "a" {
		t.Errorf("after initial replace, list = %v, want [a]", got)
	}
	entryA, _ := mgr.Get("a")

	// Add server "b" + rotate "a"'s token. Keep "a"'s upstream
	// unchanged so we can confirm the entry pointer survives.
	rolled := &auth.Servers{Servers: map[string]auth.Server{
		"a": {Upstream: upA, Auth: auth.Auth{Type: "bearer", Token: "tok-2"}},
		"b": {Upstream: upB, Auth: auth.Auth{Type: "bearer", Token: "tok-b"}},
	}}
	if err := mgr.Replace(ctx, rolled); err != nil {
		t.Fatalf("rotate replace: %v", err)
	}
	gotA, _ := mgr.Get("a")
	if gotA != entryA {
		t.Errorf("entry for 'a' was reconstructed (got %p, was %p) — token-only change should swap the resolver in place", gotA, entryA)
	}
	if gotA.AuthRef != "tok-2" {
		t.Errorf("'a' AuthRef = %q, want tok-2", gotA.AuthRef)
	}
	if _, err := mgr.Get("b"); err != nil {
		t.Errorf("server 'b' should be present after replace, got %v", err)
	}

	// Drop "b"; confirm only "a" survives.
	dropped := &auth.Servers{Servers: map[string]auth.Server{
		"a": {Upstream: upA, Auth: auth.Auth{Type: "bearer", Token: "tok-2"}},
	}}
	if err := mgr.Replace(ctx, dropped); err != nil {
		t.Fatalf("drop replace: %v", err)
	}
	if _, err := mgr.Get("b"); !errors.Is(err, ErrUnknownServer) {
		t.Errorf("'b' should be gone after drop replace, got %v", err)
	}
}

// TestReplaceRebuildsOnUpstreamURLChange asserts that changing the
// upstream URL forces a re-Add (new index, new client) — the
// cached index for the old URL would be stale.
func TestReplaceRebuildsOnUpstreamURLChange(t *testing.T) {
	ctx := context.Background()
	mgr, _ := New(t.TempDir(), 0)
	defer func() { _ = mgr.Close() }()

	upOld := fakeUpstream(t, "old_field")
	upNew := fakeUpstream(t, "new_field")

	_ = mgr.Replace(ctx, &auth.Servers{Servers: map[string]auth.Server{
		"a": {Upstream: upOld},
	}})
	before, _ := mgr.Get("a")

	_ = mgr.Replace(ctx, &auth.Servers{Servers: map[string]auth.Server{
		"a": {Upstream: upNew},
	}})
	after, _ := mgr.Get("a")

	if before == after {
		t.Errorf("URL change should produce a fresh Entry pointer (got same %p)", before)
	}
	if after.Upstream != upNew {
		t.Errorf("after URL change, Upstream = %q, want %q", after.Upstream, upNew)
	}
	// The new index should contain the new field name.
	results, _ := after.Store.Search(ctx, "new_field", 5)
	if len(results) == 0 {
		t.Errorf("new index missing new_field — did the re-introspect happen?")
	}
}

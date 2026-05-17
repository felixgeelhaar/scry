package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/felixgeelhaar/scry/internal/auth"
)

// TestWatchServersReloadsOnWrite drives the full hot-reload path:
// start the watcher, modify servers.yml, assert the Manager picks
// up the new server before the timeout fires.
func TestWatchServersReloadsOnWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	path := filepath.Join(dir, "servers.yml")
	upA := fakeUpstream(t, "alpha")
	upB := fakeUpstream(t, "beta")

	// Seed initial file with one server.
	initial := &auth.Servers{Servers: map[string]auth.Server{
		"a": {Upstream: upA, Auth: auth.Auth{Type: "bearer", Token: "t1"}},
	}}
	if err := auth.Save(initial, path); err != nil {
		t.Fatalf("save initial: %v", err)
	}

	mgr, _ := New(t.TempDir(), 0)
	defer func() { _ = mgr.Close() }()
	if err := mgr.LoadFromServers(ctx, initial); err != nil {
		t.Fatalf("load initial: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- WatchServers(ctx, path, mgr) }()
	// Give the watcher time to install its inotify hook.
	time.Sleep(100 * time.Millisecond)

	// Rewrite the file with two servers.
	updated := &auth.Servers{Servers: map[string]auth.Server{
		"a": {Upstream: upA, Auth: auth.Auth{Type: "bearer", Token: "t1"}},
		"b": {Upstream: upB, Auth: auth.Auth{Type: "bearer", Token: "t2"}},
	}}
	if err := auth.Save(updated, path); err != nil {
		t.Fatalf("save updated: %v", err)
	}

	// Wait for the debounce window + room for introspection.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := mgr.Get("b"); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := mgr.Get("b"); err != nil {
		t.Fatalf("watcher did not pick up new server 'b' within timeout: %v", err)
	}
	cancel()
	<-done
}

// TestWatchServersHandlesAtomicReplace mimics editor save-via-rename
// (the common case: vim, gopls, sed -i). The watcher must observe
// the new file even though the original inode was unlinked.
func TestWatchServersHandlesAtomicReplace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	path := filepath.Join(dir, "servers.yml")
	upA := fakeUpstream(t, "alpha")
	upB := fakeUpstream(t, "beta")

	_ = auth.Save(&auth.Servers{Servers: map[string]auth.Server{
		"a": {Upstream: upA},
	}}, path)

	mgr, _ := New(t.TempDir(), 0)
	defer func() { _ = mgr.Close() }()
	_ = mgr.LoadFromServers(ctx, &auth.Servers{Servers: map[string]auth.Server{
		"a": {Upstream: upA},
	}})

	done := make(chan error, 1)
	go func() { done <- WatchServers(ctx, path, mgr) }()
	time.Sleep(100 * time.Millisecond)

	// Atomic-replace: write a sibling file then rename over.
	// auth.Save already does this — pass a different content set
	// so we can detect the swap.
	_ = auth.Save(&auth.Servers{Servers: map[string]auth.Server{
		"a": {Upstream: upA},
		"b": {Upstream: upB},
	}}, path)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := mgr.Get("b"); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := mgr.Get("b"); err != nil {
		t.Fatalf("atomic-replace watcher did not pick up 'b': %v", err)
	}
	cancel()
	<-done
}

// TestWatchServersIgnoresUnrelatedFiles asserts the watcher doesn't
// fire when a sibling file in the same directory changes. Important
// because we watch the directory (to survive atomic rename) but
// only care about one leaf.
func TestWatchServersIgnoresUnrelatedFiles(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	path := filepath.Join(dir, "servers.yml")
	upA := fakeUpstream(t, "alpha")
	_ = auth.Save(&auth.Servers{Servers: map[string]auth.Server{"a": {Upstream: upA}}}, path)

	mgr, _ := New(t.TempDir(), 0)
	defer func() { _ = mgr.Close() }()
	_ = mgr.LoadFromServers(ctx, &auth.Servers{Servers: map[string]auth.Server{"a": {Upstream: upA}}})
	startingPtr, _ := mgr.Get("a")

	done := make(chan error, 1)
	go func() { done <- WatchServers(ctx, path, mgr) }()
	time.Sleep(100 * time.Millisecond)

	// Touch an unrelated sibling. Should NOT cause any Replace
	// activity. We poll for a fixed window to confirm nothing
	// changed.
	if err := os.WriteFile(filepath.Join(dir, "other.txt"), []byte("noise"), 0o600); err != nil {
		t.Fatalf("write unrelated: %v", err)
	}
	time.Sleep(debounceWindow + 200*time.Millisecond)

	endingPtr, _ := mgr.Get("a")
	if startingPtr != endingPtr {
		t.Errorf("Manager state mutated on unrelated file write — entry pointer changed")
	}
	cancel()
	<-done
}

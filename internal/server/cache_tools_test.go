package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	mcp "go.klarlabs.de/mcp"

	"github.com/felixgeelhaar/scry/internal/runtime"
)

// newCacheFixture stands up a single-server scry instance with the
// result cache wired (CacheTTL>0, MaxEntries>0). Returns the
// Manager + the registered cache tools' Execute thunks.
func newCacheFixture(t *testing.T) (*runtime.Manager, *mcp.Server) {
	t.Helper()
	indexDir := t.TempDir()
	mgr, err := runtime.New(indexDir, 1000)
	if err != nil {
		t.Fatalf("mgr: %v", err)
	}
	mgr.CacheTTL = time.Minute
	mgr.CacheMaxEntries = 10
	t.Cleanup(func() { _ = mgr.Close() })

	// Stand up a minimal upstream so Add succeeds. The bench
	// only cares about the cache plumbing — no live queries.
	upstreamURL := startFakeUpstream(t)
	if err := mgr.Add(context.Background(), runtime.AddConfig{
		Name:     "default",
		Upstream: upstreamURL,
	}); err != nil {
		t.Fatalf("add: %v", err)
	}

	srv := mcp.NewServer(mcp.ServerInfo{Name: "test", Version: "0"})
	if err := registerCacheTools(srv, mgr); err != nil {
		t.Fatalf("register: %v", err)
	}
	return mgr, srv
}

func TestCacheStatsEmptyCache(t *testing.T) {
	_, srv := newCacheFixture(t)
	tool, _ := srv.GetTool("cache_stats")
	in, _ := json.Marshal(map[string]any{})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	text, _ := out.(string)
	var got map[string]any
	_ = json.Unmarshal([]byte(text), &got)
	caches := got["caches"].([]any)
	if len(caches) != 1 {
		t.Fatalf("expected 1 cache entry, got %d (%+v)", len(caches), caches)
	}
	first := caches[0].(map[string]any)
	if first["server"] != "default" {
		t.Errorf("server = %v, want default", first["server"])
	}
	stats := first["stats"].(map[string]any)
	if stats["entries"].(float64) != 0 {
		t.Errorf("expected 0 entries, got %v", stats["entries"])
	}
}

func TestCacheStatsHitsAndMisses(t *testing.T) {
	mgr, srv := newCacheFixture(t)
	entry, _ := mgr.Get("default")
	entry.Cache.Set("k1", []byte("v1"))
	// Hit + miss to populate counters.
	_, _ = entry.Cache.Get("k1") // hit
	_, _ = entry.Cache.Get("k2") // miss

	tool, _ := srv.GetTool("cache_stats")
	in, _ := json.Marshal(map[string]any{})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	text, _ := out.(string)
	var got map[string]any
	_ = json.Unmarshal([]byte(text), &got)
	first := got["caches"].([]any)[0].(map[string]any)
	stats := first["stats"].(map[string]any)
	if stats["hits"].(float64) != 1 {
		t.Errorf("hits = %v, want 1", stats["hits"])
	}
	if stats["misses"].(float64) != 1 {
		t.Errorf("misses = %v, want 1", stats["misses"])
	}
	if first["hit_rate"].(float64) != 0.5 {
		t.Errorf("hit_rate = %v, want 0.5", first["hit_rate"])
	}
}

func TestCachePurgeWipesEntries(t *testing.T) {
	mgr, srv := newCacheFixture(t)
	entry, _ := mgr.Get("default")
	entry.Cache.Set("k1", []byte("v1"))
	entry.Cache.Set("k2", []byte("v2"))

	tool, _ := srv.GetTool("cache_purge")
	in, _ := json.Marshal(map[string]any{"server": "default"})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	text, _ := out.(string)
	var got map[string]any
	_ = json.Unmarshal([]byte(text), &got)
	purged := got["purged"].([]any)
	if len(purged) != 1 {
		t.Fatalf("expected 1 purge entry, got %d", len(purged))
	}
	first := purged[0].(map[string]any)
	if first["entries_purged"].(float64) != 2 {
		t.Errorf("entries_purged = %v, want 2", first["entries_purged"])
	}
	// Cache should be empty after purge.
	if entry.Cache.Len() != 0 {
		t.Errorf("cache not empty after purge: %d", entry.Cache.Len())
	}
}

func TestCacheStatsDisabledCache(t *testing.T) {
	indexDir := t.TempDir()
	mgr, err := runtime.New(indexDir, 1000)
	if err != nil {
		t.Fatalf("mgr: %v", err)
	}
	// Intentionally skip mgr.CacheTTL — entries get no cache.
	t.Cleanup(func() { _ = mgr.Close() })

	upstreamURL := startFakeUpstream(t)
	_ = mgr.Add(context.Background(), runtime.AddConfig{
		Name: "default", Upstream: upstreamURL,
	})

	srv := mcp.NewServer(mcp.ServerInfo{Name: "test", Version: "0"})
	_ = registerCacheTools(srv, mgr)

	tool, _ := srv.GetTool("cache_stats")
	in, _ := json.Marshal(map[string]any{})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	text, _ := out.(string)
	var got map[string]any
	_ = json.Unmarshal([]byte(text), &got)
	first := got["caches"].([]any)[0].(map[string]any)
	if disabled, _ := first["disabled"].(bool); !disabled {
		t.Errorf("expected disabled=true for cache-less entry, got %+v", first)
	}
}

package server

import (
	"context"
	"encoding/json"

	mcp "go.klarlabs.de/mcp"

	"github.com/felixgeelhaar/scry/internal/cache"
	"github.com/felixgeelhaar/scry/internal/runtime"
)

// registerCacheTools wires the operator-facing cache observability +
// admin tools: cache_stats (read-only snapshot) and cache_purge
// (admin-only wipe). Both are gated behind requireAdmin so read-only
// clients can't poke at server-internal counters or invalidate
// state for the agents sharing the deployment.
//
//nolint:unparam
func registerCacheTools(srv *mcp.Server, mgr *runtime.Manager) error {
	type StatsInput struct {
		Server string `json:"server,omitempty" jsonschema:"description=optional server name; omit for every cache"`
	}
	srv.Tool("cache_stats").
		Description(descCacheStats).
		Handler(func(ctx context.Context, in StatsInput) (string, error) {
			if denied := requireAdmin(ctx, "cache_stats"); denied != "" {
				return denied, nil
			}
			out := map[string]any{"caches": collectCacheStats(mgr, in.Server)}
			enc, _ := json.MarshalIndent(out, "", "  ")
			return string(enc), nil
		})

	type PurgeInput struct {
		Server string `json:"server,omitempty" jsonschema:"description=server to purge; omit to purge every cache"`
	}
	srv.Tool("cache_purge").
		Description(descCachePurge).
		Handler(func(ctx context.Context, in PurgeInput) (string, error) {
			if denied := requireAdmin(ctx, "cache_purge"); denied != "" {
				return denied, nil
			}
			out := map[string]any{"purged": runCachePurge(mgr, in.Server)}
			enc, _ := json.MarshalIndent(out, "", "  ")
			return string(enc), nil
		})
	return nil
}

// collectCacheStats walks the Manager's entries and returns a slice
// of {server, stats}. Entries with no cache configured render with
// disabled=true so operators can spot misconfigured upstreams (cache
// expected, not wired).
func collectCacheStats(mgr *runtime.Manager, only string) []map[string]any {
	names := mgr.List()
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		if only != "" && n != only {
			continue
		}
		e, err := mgr.Get(n)
		if err != nil {
			continue
		}
		entry := map[string]any{"server": n}
		if e.Cache == nil {
			entry["disabled"] = true
			out = append(out, entry)
			continue
		}
		stats := e.Cache.Stats()
		entry["stats"] = stats
		entry["hit_rate"] = computeHitRate(stats)
		out = append(out, entry)
	}
	return out
}

// runCachePurge wipes one or every cache and returns a per-server
// entry-count summary suitable for the response envelope.
func runCachePurge(mgr *runtime.Manager, only string) []map[string]any {
	names := mgr.List()
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		if only != "" && n != only {
			continue
		}
		e, err := mgr.Get(n)
		if err != nil {
			continue
		}
		entry := map[string]any{"server": n}
		if e.Cache == nil {
			entry["disabled"] = true
			out = append(out, entry)
			continue
		}
		entry["entries_purged"] = e.Cache.Purge()
		out = append(out, entry)
	}
	return out
}

func computeHitRate(s cache.Stats) float64 {
	total := s.Hits + s.Misses
	if total == 0 {
		return 0
	}
	return float64(s.Hits) / float64(total)
}

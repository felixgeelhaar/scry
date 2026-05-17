package server

import (
	"context"
	"encoding/json"

	mcp "github.com/felixgeelhaar/mcp-go"

	"github.com/felixgeelhaar/scry/internal/runtime"
)

// registerRuntimeTools wires the meta tools that describe what's
// configured at runtime — distinct from auth_status (which reports
// credential health) because list_servers is callable even when a
// server has no token at all (e.g. a public upstream).
//
// Returned shape gives agents everything they need to pick a target
// before calling the per-server tools: name + upstream URL.
func registerRuntimeTools(srv *mcp.Server, mgr *runtime.Manager) error {
	type Empty struct{}
	srv.Tool("list_servers").
		Description("List every upstream scry can route to. Call this first to discover which `server` value to pass to schema_search / query_execute / etc. Returns name + upstream URL per entry; never returns secrets.").
		Handler(func(_ context.Context, _ Empty) (string, error) {
			names := mgr.List()
			out := make([]map[string]string, 0, len(names))
			for _, n := range names {
				e, err := mgr.Get(n)
				if err != nil {
					continue
				}
				out = append(out, map[string]string{
					"name":     e.Name,
					"upstream": e.Upstream,
				})
			}
			enc, _ := json.MarshalIndent(map[string]any{"servers": out}, "", "  ")
			return string(enc), nil
		})
	return nil
}

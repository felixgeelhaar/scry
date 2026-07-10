package server

import (
	"context"

	mcp "go.klarlabs.de/mcp"

	"github.com/felixgeelhaar/scry/internal/runtime"
)

// ServerRef identifies one configured upstream in list_servers output.
type ServerRef struct {
	Name     string `json:"name"`
	Upstream string `json:"upstream"`
}

// ListServersResult is the structured output of list_servers: the set
// of configured upstreams an agent can target.
type ListServersResult struct {
	Servers []ServerRef `json:"servers"`
}

// registerRuntimeTools wires the meta tools that describe what's
// configured at runtime — distinct from auth_status (which reports
// credential health) because list_servers is callable even when a
// server has no token at all (e.g. a public upstream).
//
// Returned shape gives agents everything they need to pick a target
// before calling the per-server tools: name + upstream URL.
//
//nolint:unparam // symmetry with other register*Tools — future wiring may fail
func registerRuntimeTools(srv *mcp.Server, mgr *runtime.Manager) error {
	type Empty struct{}
	srv.Tool("list_servers").
		Description(descListServers).
		OutputSchema(ListServersResult{}).
		Handler(func(_ context.Context, _ Empty) (ListServersResult, error) {
			names := mgr.List()
			out := make([]ServerRef, 0, len(names))
			for _, n := range names {
				e, err := mgr.Get(n)
				if err != nil {
					continue
				}
				out = append(out, ServerRef{Name: e.Name, Upstream: e.Upstream})
			}
			return ListServersResult{Servers: out}, nil
		})
	return nil
}

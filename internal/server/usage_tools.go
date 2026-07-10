package server

import (
	"context"

	mcp "go.klarlabs.de/mcp"

	"github.com/felixgeelhaar/scry/internal/usage"
)

// UsageStatsResult is the structured output of usage_stats: the
// per-tenant/session metering snapshots, optionally filtered to one
// tenant.
type UsageStatsResult struct {
	Tenant  string           `json:"tenant"`
	Records []usage.Snapshot `json:"records"`
}

// registerUsageTools wires the usage_stats admin-facing tool. The
// process-level Tracker is injected — scry's Run() builds one at
// boot + threads it through every query_execute / etc. handler
// site that needs to bump counters. v0.7 ships the tool surface;
// the per-handler instrumentation is wired incrementally as the
// metering layer matures.
//
//nolint:unparam
func registerUsageTools(srv *mcp.Server, tracker *usage.Tracker) error {
	if tracker == nil {
		// No-op registration when metering is disabled — operators
		// who don't care about $$ accounting don't pay for it.
		return nil
	}
	type StatsInput struct {
		Tenant string `json:"tenant,omitempty" jsonschema:"description=optional tenant filter; omit to surface every cell"`
	}
	srv.Tool("usage_stats").
		Description(descUsageStats).
		OutputSchema(UsageStatsResult{}).
		Handler(func(ctx context.Context, in StatsInput) (any, error) {
			if denied := requireAdmin(ctx, "usage_stats"); denied != "" {
				return denied, nil
			}
			snap := tracker.Snapshot(in.Tenant)
			return UsageStatsResult{Tenant: in.Tenant, Records: snap}, nil
		})
	return nil
}

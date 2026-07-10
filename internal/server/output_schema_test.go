package server

import (
	"testing"

	mcpschema "go.klarlabs.de/mcp/schema"

	scryschema "github.com/felixgeelhaar/scry/internal/schema"
)

// TestOutputSchemasGenerate guards every type advertised via a tool's
// .OutputSchema(...) call. mcp-go runs schema.Generate at registration
// time; if generation fails there, the ToolBuilder records the error
// and the tool is silently dropped from the server — a failure that is
// invisible until a client notices the tool is missing. This test
// exercises schema.Generate directly for each advertised output type so
// a regression (e.g. an added field of an unsupported kind) surfaces as
// a red test rather than a vanished tool.
func TestOutputSchemasGenerate(t *testing.T) {
	cases := []struct {
		tool string
		typ  any
	}{
		{"list_servers", ListServersResult{}},
		{"gate_status", GateStatus{}},
		{"gate_chain", GateChainResult{}},
		{"query_cost", scryschema.CostReport{}},
		{"schema_neighbors", NeighborsResult{}},
		{"usage_stats", UsageStatsResult{}},
		{"auth_status", AuthStatusResult{}},
		{"schema_webhooks_list", WebhooksListResult{}},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			if _, err := mcpschema.Generate(tc.typ); err != nil {
				t.Fatalf("schema.Generate for %s output type %T failed: %v", tc.tool, tc.typ, err)
			}
		})
	}
}

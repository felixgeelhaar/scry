package server

import (
	"context"
	"encoding/json"

	mcp "go.klarlabs.de/mcp"

	"github.com/felixgeelhaar/scry/internal/gate"
)

// registerGateTools wires gate_status, the audit + budget read tool.
// Agents call it to see how much of their session budget remains
// before launching a long workflow; operators read it through the
// MCP host's UI for live audit.
//
// gate_chain returns the audit chain itself — meant for debugging
// and external audit-pipeline consumers, not normal agent use.
// Returns an error for symmetry with the other register*Tools
// funcs; today no tool wiring can fail.
//
//nolint:unparam // see registerAuthTools rationale
func registerGateTools(srv *mcp.Server, g *gate.Gate) error {
	type Empty struct{}
	srv.Tool("gate_status").
		Description(descGateStatus).
		Handler(func(ctx context.Context, _ Empty) (string, error) {
			session := sessionFromContext(ctx)
			stats := g.Stats(session)
			enc, _ := json.MarshalIndent(map[string]any{
				"session":               string(session),
				"writes":                stats.Writes,
				"cumulative_complexity": stats.Complexity,
				"chain_length":          stats.ChainLen,
			}, "", "  ")
			return string(enc), nil
		})

	type ChainInput struct {
		Verify bool `json:"verify,omitempty" jsonschema:"description=run VerifyChain over the returned records and include the result"`
		Limit  int  `json:"limit,omitempty"  jsonschema:"description=cap on records returned (newest first); 0 returns all,maximum=10000"`
	}
	srv.Tool("gate_chain").
		Description(descGateChain).
		Handler(func(ctx context.Context, in ChainInput) (string, error) {
			session := sessionFromContext(ctx)
			chain := g.Chain(session)

			// Truncation policy: newest-first slice if limit
			// is set. Verification still runs over the FULL
			// chain (truncating would invalidate the hash links
			// of trimmed records). Operators verifying a
			// tamper claim need the whole chain — the limit
			// only affects what the agent gets in its context
			// window.
			result := map[string]any{
				"session":      string(session),
				"chain_length": len(chain),
			}
			if in.Verify {
				bad, err := gate.VerifyChain(chain)
				if err != nil {
					result["verified"] = false
					result["first_bad_index"] = bad
					result["verify_error"] = err.Error()
				} else {
					result["verified"] = true
				}
			}

			out := chain
			if in.Limit > 0 && len(chain) > in.Limit {
				out = chain[len(chain)-in.Limit:]
				result["truncated"] = true
				result["returned"] = len(out)
			}
			result["records"] = out
			enc, _ := json.MarshalIndent(result, "", "  ")
			return string(enc), nil
		})
	return nil
}

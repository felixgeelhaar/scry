package server

import (
	"context"

	mcp "go.klarlabs.de/mcp"

	"github.com/felixgeelhaar/scry/internal/gate"
)

// GateStatus is the structured output of gate_status: the caller's
// session budget + audit-chain counters.
type GateStatus struct {
	Session              string `json:"session"`
	Writes               int    `json:"writes"`
	CumulativeComplexity int    `json:"cumulative_complexity"`
	ChainLength          int    `json:"chain_length"`
}

// GateChainResult is the structured output of gate_chain: the audit
// evidence chain plus optional verification + truncation metadata.
// Verified/FirstBadIndex/VerifyError are populated only when the
// caller requests verification; Truncated/Returned only when a limit
// trims the returned slice.
type GateChainResult struct {
	Session       string          `json:"session"`
	ChainLength   int             `json:"chain_length"`
	Verified      *bool           `json:"verified,omitempty"`
	FirstBadIndex *int            `json:"first_bad_index,omitempty"`
	VerifyError   string          `json:"verify_error,omitempty"`
	Truncated     bool            `json:"truncated,omitempty"`
	Returned      int             `json:"returned,omitempty"`
	Records       []gate.Evidence `json:"records"`
}

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
		OutputSchema(GateStatus{}).
		Handler(func(ctx context.Context, _ Empty) (GateStatus, error) {
			session := sessionFromContext(ctx)
			stats := g.Stats(session)
			return GateStatus{
				Session:              string(session),
				Writes:               stats.Writes,
				CumulativeComplexity: stats.Complexity,
				ChainLength:          stats.ChainLen,
			}, nil
		})

	type ChainInput struct {
		Verify bool `json:"verify,omitempty" jsonschema:"description=run VerifyChain over the returned records and include the result"`
		Limit  int  `json:"limit,omitempty"  jsonschema:"description=cap on records returned (newest first); 0 returns all,maximum=10000"`
	}
	srv.Tool("gate_chain").
		Description(descGateChain).
		OutputSchema(GateChainResult{}).
		Handler(func(ctx context.Context, in ChainInput) (GateChainResult, error) {
			session := sessionFromContext(ctx)
			chain := g.Chain(session)

			// Truncation policy: newest-first slice if limit
			// is set. Verification still runs over the FULL
			// chain (truncating would invalidate the hash links
			// of trimmed records). Operators verifying a
			// tamper claim need the whole chain — the limit
			// only affects what the agent gets in its context
			// window.
			result := GateChainResult{
				Session:     string(session),
				ChainLength: len(chain),
			}
			if in.Verify {
				bad, err := gate.VerifyChain(chain)
				if err != nil {
					verified := false
					badIdx := bad
					result.Verified = &verified
					result.FirstBadIndex = &badIdx
					result.VerifyError = err.Error()
				} else {
					verified := true
					result.Verified = &verified
				}
			}

			out := chain
			if in.Limit > 0 && len(chain) > in.Limit {
				out = chain[len(chain)-in.Limit:]
				result.Truncated = true
				result.Returned = len(out)
			}
			result.Records = out
			return result, nil
		})
	return nil
}

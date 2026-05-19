// Package usage is scry's per-session + per-tenant metering layer.
// Aggregates the audit chain's events into counters operators can
// surface via the usage_stats MCP tool or scrape via /metrics —
// the data needed for usage-based billing.
//
// Counters are in-memory only at v0.7; pg-backed persistence
// lands alongside the pg-store + redis-cache work in v0.8 so
// per-tenant counters survive restarts in HA deployments.
package usage

import (
	"sync"
	"time"
)

// Counters tracks one cell in the metering matrix:
// {tenant, session_id} → aggregated counts. Safe for concurrent
// use under the package-level Tracker mutex.
type Counters struct {
	ToolCalls          map[string]int64 // tool name → count
	ToolCallsByOutcome map[string]int64 // "<tool>|<outcome>" → count
	UpstreamBytesIn    int64
	UpstreamBytesOut   int64
	UpstreamLatencyMs  int64 // sum; divide by ToolCalls["query_execute"] for avg
	ComplexityConsumed int64
	DollarsConsumed    float64
	FirstSeen          time.Time
	LastSeen           time.Time
}

// Snapshot is a copy of a Counters as it is at one point in time.
// Used by Stats() to return a value that callers can hold past the
// tracker mutex.
type Snapshot struct {
	Tenant             string           `json:"tenant"`
	Session            string           `json:"session"`
	ToolCalls          map[string]int64 `json:"tool_calls"`
	ToolCallsByOutcome map[string]int64 `json:"tool_calls_by_outcome,omitempty"`
	UpstreamBytesIn    int64            `json:"upstream_bytes_in"`
	UpstreamBytesOut   int64            `json:"upstream_bytes_out"`
	AvgUpstreamLatency time.Duration    `json:"avg_upstream_latency,omitempty"`
	ComplexityConsumed int64            `json:"complexity_consumed"`
	DollarsConsumed    float64          `json:"dollars_consumed"`
	FirstSeen          time.Time        `json:"first_seen,omitempty"`
	LastSeen           time.Time        `json:"last_seen,omitempty"`
}

// Tracker is the in-memory counter store. One Tracker per process;
// scry boots one at startup and threads it through the tool
// handlers via the obs package alongside the metrics provider.
type Tracker struct {
	mu       sync.Mutex
	counters map[string]*Counters // key = "<tenant>|<session>"
	// dollarsPerTool optionally bills tool calls at a per-tool
	// dollar rate. Empty map disables $$ accounting; partial
	// maps charge only the listed tools. Operators populate via
	// SetDollarsPerTool.
	dollarsPerTool map[string]float64
}

// NewTracker returns an empty Tracker.
func NewTracker() *Tracker {
	return &Tracker{counters: map[string]*Counters{}}
}

// SetDollarsPerTool installs a cost table. Calling this twice
// REPLACES the table — partial updates must include every prior
// entry the operator wants kept.
func (t *Tracker) SetDollarsPerTool(table map[string]float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dollarsPerTool = make(map[string]float64, len(table))
	for k, v := range table {
		t.dollarsPerTool[k] = v
	}
}

// RecordToolCall bumps the per-tool + per-outcome counts. Outcome
// is the same enum query_execute / friends emit ("ok",
// "permission_denied", "rate_limited", etc.). DollarsPerTool, if
// set, is charged at the tool's published rate.
func (t *Tracker) RecordToolCall(tenant, session, tool, outcome string) {
	c := t.cellLocked(tenant, session)
	c.ToolCalls[tool]++
	c.ToolCallsByOutcome[tool+"|"+outcome]++
	if rate, ok := t.dollarsPerTool[tool]; ok {
		c.DollarsConsumed += rate
	}
	c.LastSeen = time.Now()
}

// RecordUpstream is called by query_execute on a successful upstream
// POST: bytes in/out + latency. Negative inputs are clamped to 0
// rather than rejected — the counter must stay monotonic.
func (t *Tracker) RecordUpstream(tenant, session string, bytesIn, bytesOut int64, latencyMs int64) {
	if bytesIn < 0 {
		bytesIn = 0
	}
	if bytesOut < 0 {
		bytesOut = 0
	}
	if latencyMs < 0 {
		latencyMs = 0
	}
	c := t.cellLocked(tenant, session)
	c.UpstreamBytesIn += bytesIn
	c.UpstreamBytesOut += bytesOut
	c.UpstreamLatencyMs += latencyMs
	c.LastSeen = time.Now()
}

// RecordComplexity bumps the cumulative complexity counter — every
// query_execute call adds its computed complexity, even on the
// cached path (cached calls still consumed schema-cost budget).
func (t *Tracker) RecordComplexity(tenant, session string, complexity int) {
	if complexity < 0 {
		return
	}
	c := t.cellLocked(tenant, session)
	c.ComplexityConsumed += int64(complexity)
	c.LastSeen = time.Now()
}

// cellLocked returns the Counters for a (tenant, session) pair,
// creating it on first access. Caller MUST NOT hold t.mu — the
// method takes + releases it.
func (t *Tracker) cellLocked(tenant, session string) *Counters {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.unlockedCell(tenant, session)
}

func (t *Tracker) unlockedCell(tenant, session string) *Counters {
	key := tenant + "|" + session
	c, ok := t.counters[key]
	if !ok {
		c = &Counters{
			ToolCalls:          map[string]int64{},
			ToolCallsByOutcome: map[string]int64{},
			FirstSeen:          time.Now(),
		}
		t.counters[key] = c
	}
	return c
}

// Snapshot returns a copy of every per-(tenant, session) cell.
// Optional `tenant` filter — empty means every tenant. Used by the
// usage_stats MCP tool + the Prometheus collector.
func (t *Tracker) Snapshot(tenant string) []Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Snapshot, 0, len(t.counters))
	for key, c := range t.counters {
		// key is "<tenant>|<session>"
		ten, sess := splitKey(key)
		if tenant != "" && tenant != ten {
			continue
		}
		var avgLatency time.Duration
		if calls := c.ToolCalls["query_execute"]; calls > 0 {
			avgLatency = time.Duration(c.UpstreamLatencyMs/calls) * time.Millisecond
		}
		out = append(out, Snapshot{
			Tenant:             ten,
			Session:            sess,
			ToolCalls:          copyMap(c.ToolCalls),
			ToolCallsByOutcome: copyMap(c.ToolCallsByOutcome),
			UpstreamBytesIn:    c.UpstreamBytesIn,
			UpstreamBytesOut:   c.UpstreamBytesOut,
			AvgUpstreamLatency: avgLatency,
			ComplexityConsumed: c.ComplexityConsumed,
			DollarsConsumed:    c.DollarsConsumed,
			FirstSeen:          c.FirstSeen,
			LastSeen:           c.LastSeen,
		})
	}
	return out
}

func splitKey(k string) (tenant, session string) {
	for i := 0; i < len(k); i++ {
		if k[i] == '|' {
			return k[:i], k[i+1:]
		}
	}
	return k, ""
}

func copyMap(m map[string]int64) map[string]int64 {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

package usage

import (
	"testing"
)

func TestRecordToolCallCountsByOutcome(t *testing.T) {
	tr := NewTracker()
	tr.RecordToolCall("default", "s1", "query_execute", "ok")
	tr.RecordToolCall("default", "s1", "query_execute", "ok")
	tr.RecordToolCall("default", "s1", "query_execute", "permission_denied")
	snap := tr.Snapshot("")
	if len(snap) != 1 {
		t.Fatalf("expected 1 snapshot row, got %d", len(snap))
	}
	if got := snap[0].ToolCalls["query_execute"]; got != 3 {
		t.Errorf("tool_calls[query_execute] = %d, want 3", got)
	}
	if got := snap[0].ToolCallsByOutcome["query_execute|ok"]; got != 2 {
		t.Errorf("query_execute|ok = %d, want 2", got)
	}
	if got := snap[0].ToolCallsByOutcome["query_execute|permission_denied"]; got != 1 {
		t.Errorf("query_execute|permission_denied = %d, want 1", got)
	}
}

func TestRecordUpstreamSumsBytesAndLatency(t *testing.T) {
	tr := NewTracker()
	tr.RecordUpstream("default", "s1", 100, 200, 50)
	tr.RecordUpstream("default", "s1", 50, 100, 30)
	tr.RecordToolCall("default", "s1", "query_execute", "ok")
	tr.RecordToolCall("default", "s1", "query_execute", "ok")
	snap := tr.Snapshot("")
	if snap[0].UpstreamBytesIn != 150 {
		t.Errorf("bytes_in = %d, want 150", snap[0].UpstreamBytesIn)
	}
	if snap[0].UpstreamBytesOut != 300 {
		t.Errorf("bytes_out = %d, want 300", snap[0].UpstreamBytesOut)
	}
	// 80ms total / 2 query_execute calls = 40ms avg.
	if snap[0].AvgUpstreamLatency.Milliseconds() != 40 {
		t.Errorf("avg latency = %v, want 40ms", snap[0].AvgUpstreamLatency)
	}
}

func TestRecordUpstreamClampsNegatives(t *testing.T) {
	tr := NewTracker()
	tr.RecordUpstream("default", "s1", -5, -10, -100)
	snap := tr.Snapshot("")
	if snap[0].UpstreamBytesIn != 0 || snap[0].UpstreamBytesOut != 0 {
		t.Errorf("negative inputs must clamp to 0; got %+v", snap[0])
	}
}

func TestDollarsBilledFromCostTable(t *testing.T) {
	tr := NewTracker()
	tr.SetDollarsPerTool(map[string]float64{
		"query_execute": 0.001,
		"schema_search": 0.0001,
	})
	tr.RecordToolCall("default", "s1", "query_execute", "ok")
	tr.RecordToolCall("default", "s1", "schema_search", "ok")
	tr.RecordToolCall("default", "s1", "query_execute", "ok")
	snap := tr.Snapshot("")
	// 2 * 0.001 + 1 * 0.0001 = 0.0021
	if got := snap[0].DollarsConsumed; got < 0.00209 || got > 0.00211 {
		t.Errorf("dollars = %v, want ~0.0021", got)
	}
}

func TestDollarsUnbilledToolsCostZero(t *testing.T) {
	tr := NewTracker()
	tr.SetDollarsPerTool(map[string]float64{"query_execute": 0.001})
	// Tool not in the cost table → no $$ charged.
	tr.RecordToolCall("default", "s1", "schema_neighbors", "ok")
	snap := tr.Snapshot("")
	if snap[0].DollarsConsumed != 0 {
		t.Errorf("unbilled tool charged: %v", snap[0].DollarsConsumed)
	}
}

func TestSnapshotFiltersByTenant(t *testing.T) {
	tr := NewTracker()
	tr.RecordToolCall("acme", "s1", "query_execute", "ok")
	tr.RecordToolCall("globex", "s2", "query_execute", "ok")
	if len(tr.Snapshot("")) != 2 {
		t.Errorf("expected 2 cells when no tenant filter")
	}
	acme := tr.Snapshot("acme")
	if len(acme) != 1 || acme[0].Tenant != "acme" {
		t.Errorf("acme filter wrong: %+v", acme)
	}
	if len(tr.Snapshot("missing")) != 0 {
		t.Errorf("nonexistent tenant must return empty")
	}
}

func TestSnapshotCopiesCounterMaps(t *testing.T) {
	// Mutating the returned ToolCalls map MUST NOT affect the
	// tracker's underlying counter.
	tr := NewTracker()
	tr.RecordToolCall("default", "s1", "query_execute", "ok")
	snap := tr.Snapshot("")
	snap[0].ToolCalls["query_execute"] = 9999
	snap2 := tr.Snapshot("")
	if snap2[0].ToolCalls["query_execute"] != 1 {
		t.Errorf("snapshot mutation leaked into tracker: %d", snap2[0].ToolCalls["query_execute"])
	}
}

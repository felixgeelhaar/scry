package gate

import (
	"path/filepath"
	"testing"
)

// mustNew is a test helper that fails the test on Gate-construction
// errors so existing in-memory cases (no AuditDir) stay terse.
func mustNew(t *testing.T, p Policy) *Gate {
	t.Helper()
	g, err := New(p)
	if err != nil {
		t.Fatalf("gate.New: %v", err)
	}
	return g
}

func TestClassifyDetectsMutationAndQuery(t *testing.T) {
	cases := map[string]Effect{
		`{ allFilms { films { title } } }`:                  EffectRead,
		`query All { allFilms { films { title } } }`:        EffectRead,
		`mutation Add { addBook(title: "x") { id } }`:       EffectWrite,
		`subscription Watch { onUpdate { id } }`:            EffectSubscribe,
		`query A { ping } mutation B { setX(v: 1) { id } }`: EffectWrite, // promoted
		``:                                                  EffectRead,
		`malformed garbage {{{`:                             EffectRead, // never silently escalate
	}
	for q, want := range cases {
		if got := Classify(q); got != want {
			t.Errorf("Classify(%q) = %s, want %s", q, got, want)
		}
	}
}

func TestCheckBudgetAllowsWhenUnlimited(t *testing.T) {
	g := mustNew(t, Policy{})
	d := g.CheckBudget("s1", EffectWrite, 100)
	if !d.Allowed {
		t.Errorf("zero policy should allow everything, got %+v", d)
	}
	if d.Remaining["writes_remaining"] != -1 {
		t.Errorf("unlimited should report -1, got %v", d.Remaining)
	}
}

func TestCheckBudgetDeniesWritesAfterLimit(t *testing.T) {
	g := mustNew(t, Policy{MaxWritesPerSession: 2})
	// Two successful writes burn the budget.
	for i := 0; i < 2; i++ {
		if d := g.CheckBudget("s1", EffectWrite, 0); !d.Allowed {
			t.Fatalf("write %d should be allowed", i)
		}
		g.Record("s1", "srv", EffectWrite, 0, "mutation X { a }", nil, "ok")
	}
	d := g.CheckBudget("s1", EffectWrite, 0)
	if d.Allowed {
		t.Errorf("third write should be denied")
	}
	if d.Reason == "" {
		t.Errorf("denial should carry a reason")
	}
}

func TestCheckBudgetDeniesComplexityOverflow(t *testing.T) {
	g := mustNew(t, Policy{MaxComplexityPerSession: 100})
	g.Record("s1", "srv", EffectRead, 80, "q", nil, "ok")
	d := g.CheckBudget("s1", EffectRead, 30)
	if d.Allowed {
		t.Errorf("80+30 > 100 should be denied, got %+v", d)
	}
	d = g.CheckBudget("s1", EffectRead, 10)
	if !d.Allowed {
		t.Errorf("80+10 <= 100 should be allowed, got %+v", d)
	}
}

func TestRecordOnlyCountsSuccessfulWrites(t *testing.T) {
	g := mustNew(t, Policy{MaxWritesPerSession: 1})
	// Failed write does NOT burn the budget — it didn't actually
	// mutate anything.
	g.Record("s1", "srv", EffectWrite, 0, "mutation X { a }", nil, "upstream_error")
	if d := g.CheckBudget("s1", EffectWrite, 0); !d.Allowed {
		t.Errorf("failed write should not burn budget, got %+v", d)
	}
}

func TestEvidenceChainIsTamperEvident(t *testing.T) {
	g := mustNew(t, Policy{})
	g.Record("s1", "srv", EffectRead, 5, "q1", []byte(`{"data":1}`), "ok")
	g.Record("s1", "srv", EffectWrite, 10, "mutation { a }", []byte(`{"data":2}`), "ok")
	g.Record("s1", "srv", EffectRead, 3, "q3", []byte(`{"data":3}`), "ok")

	chain := g.Chain("s1")
	if len(chain) != 3 {
		t.Fatalf("expected 3 records, got %d", len(chain))
	}
	if bad, err := VerifyChain(chain); err != nil {
		t.Errorf("clean chain should verify, got bad=%d err=%v", bad, err)
	}

	// Tamper with the middle record — chain hash must no longer
	// match.
	chain[1].QueryHash = "tampered"
	bad, err := VerifyChain(chain)
	if err == nil {
		t.Errorf("tampered chain should fail verification")
	}
	if bad != 1 {
		t.Errorf("verification should pinpoint index 1 (the tampered record), got %d", bad)
	}
}

func TestChainHashesAreDistinctPerRecord(t *testing.T) {
	g := mustNew(t, Policy{})
	a := g.Record("s1", "srv", EffectRead, 5, "same", []byte("same"), "ok")
	b := g.Record("s1", "srv", EffectRead, 5, "same", []byte("same"), "ok")
	if a.ChainHash == b.ChainHash {
		t.Errorf("two records with identical payload should still have distinct chain hashes (timestamp differs)")
	}
}

func TestStatsTracksWritesAndComplexity(t *testing.T) {
	g := mustNew(t, Policy{})
	g.Record("s1", "srv", EffectWrite, 50, "mutation { a }", nil, "ok")
	g.Record("s1", "srv", EffectRead, 25, "q", nil, "ok")
	st := g.Stats("s1")
	if st.Writes != 1 || st.Complexity != 75 || st.ChainLen != 2 {
		t.Errorf("Stats unexpected: %+v", st)
	}
}

func TestPersistentChainSurvivesRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")

	// First boot: write three records then close.
	g1, err := New(Policy{AuditDir: dir, MaxWritesPerSession: 5})
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	g1.Record("agent-a", "shopify", EffectWrite, 50, "mutation X { a }", []byte(`{"ok":1}`), "ok")
	g1.Record("agent-a", "shopify", EffectRead, 20, "{ q1 }", []byte(`{"ok":2}`), "ok")
	g1.Record("agent-a", "shopify", EffectRead, 30, "{ q2 }", []byte(`{"ok":3}`), "ok")
	if err := g1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Second boot: same dir. Chain + counters should replay from
	// disk.
	g2, err := New(Policy{AuditDir: dir, MaxWritesPerSession: 5})
	if err != nil {
		t.Fatalf("second New: %v", err)
	}
	defer func() { _ = g2.Close() }()
	chain := g2.Chain("agent-a")
	if len(chain) != 3 {
		t.Errorf("expected 3 records after replay, got %d", len(chain))
	}
	if bad, err := VerifyChain(chain); err != nil {
		t.Errorf("replayed chain should verify, got bad=%d err=%v", bad, err)
	}
	st := g2.Stats("agent-a")
	if st.Writes != 1 {
		t.Errorf("expected writes=1 after replay, got %d", st.Writes)
	}
	if st.Complexity != 100 {
		t.Errorf("expected complexity=100 after replay (50+20+30), got %d", st.Complexity)
	}

	// New write after restart appends to the same chain hash
	// sequence — chain must verify end-to-end including the new
	// record.
	g2.Record("agent-a", "shopify", EffectWrite, 5, "mutation Y { b }", nil, "ok")
	chain = g2.Chain("agent-a")
	if bad, err := VerifyChain(chain); err != nil {
		t.Errorf("extended chain should verify, got bad=%d err=%v", bad, err)
	}
	if g2.Stats("agent-a").Writes != 2 {
		t.Errorf("expected writes=2 after restart + 1 more write, got %d", g2.Stats("agent-a").Writes)
	}
}

func TestPersistentChainSeparatesSessions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	g, err := New(Policy{AuditDir: dir})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer func() { _ = g.Close() }()
	g.Record("alpha", "srv", EffectRead, 1, "q1", nil, "ok")
	g.Record("beta", "srv", EffectRead, 1, "q1", nil, "ok")
	if a := len(g.Chain("alpha")); a != 1 {
		t.Errorf("alpha chain = %d, want 1", a)
	}
	if b := len(g.Chain("beta")); b != 1 {
		t.Errorf("beta chain = %d, want 1", b)
	}
}

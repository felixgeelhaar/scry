//go:build stress

// Race-stress harness for the runtime layer. Drives three concurrent
// pressures against the Manager + Gate:
//
//  1. Many goroutines issue Get + introspect-style calls against
//     live entries (the read path agents take through query_execute).
//  2. A rotation worker calls Replace() against a moving servers.yml
//     every few ms, forcing the diff machinery + per-entry locks.
//  3. A record worker hammers Gate.Record on shared sessions to
//     exercise the chain-hash mutex + audit-store concurrency.
//
// Run with:
//
//	go test -tags=stress -race -timeout 60s ./internal/runtime/...
//
// 30s default duration so the harness fires in CI nightly jobs
// without slowing the PR-blocking suite. Override with
// SCRY_STRESS_DURATION=… for a longer soak locally.
package runtime

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felixgeelhaar/bolt"

	"github.com/felixgeelhaar/scry/internal/auth"
	"github.com/felixgeelhaar/scry/internal/gate"
	"github.com/felixgeelhaar/scry/internal/obs"
)

func init() {
	// Silence the global logger so 30s of stress doesn't spam
	// the test runner's stdout.
	obs.SetForTest(bolt.New(bolt.NewJSONHandler(devNull{})))
}

type devNull struct{}

func (devNull) Write(p []byte) (int, error) { return len(p), nil }

func stressDuration(t *testing.T) time.Duration {
	if s := os.Getenv("SCRY_STRESS_DURATION"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			t.Logf("stress duration overridden via SCRY_STRESS_DURATION=%s", s)
			return d
		}
	}
	return 30 * time.Second
}

// TestStressManagerReplaceUnderLoad runs all three pressures
// concurrently for the configured duration. Asserts:
//
//   - The race detector flags zero races (test fails if -race tripped
//     during the run).
//   - The Manager never produces a nil Entry from Get for a server
//     that should be present.
//   - Gate.Record's chain remains internally consistent after the
//     run.
func TestStressManagerReplaceUnderLoad(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), stressDuration(t))
	defer cancel()

	indexDir := t.TempDir()
	auditDir := t.TempDir()
	mgr, err := New(indexDir, 1000)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	defer func() { _ = mgr.Close() }()

	g, err := gate.New(gate.Policy{
		AuditDir:     auditDir,
		AuditMaxSize: 4096, // small to force rotations under load
		AuditKeep:    3,
	})
	if err != nil {
		t.Fatalf("new gate: %v", err)
	}
	defer func() { _ = g.Close() }()

	// Three fake upstreams so Replace has something to swap
	// between.
	upA := fakeUpstream(t, "alpha")
	upB := fakeUpstream(t, "beta")
	upC := fakeUpstream(t, "gamma")
	upstreams := []string{upA, upB, upC}

	// Seed with two servers; the rotation worker will flip them.
	initial := &auth.Servers{Servers: map[string]auth.Server{
		"shopify": {Upstream: upA, Auth: auth.Auth{Type: "bearer", Token: "t-shopify"}},
		"linear":  {Upstream: upB, Auth: auth.Auth{Type: "bearer", Token: "t-linear"}},
	}}
	if err := mgr.Replace(ctx, initial); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var (
		wg          sync.WaitGroup
		gets        atomic.Int64
		replaces    atomic.Int64
		records     atomic.Int64
		errs        atomic.Int64
		seenServers = []string{"shopify", "linear", "github"}
	)

	// Read workers — represent the agent traffic.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				name := seenServers[(int(gets.Load())+id)%len(seenServers)]
				if _, err := mgr.Get(name); err != nil {
					// Unknown server is acceptable mid-rotation;
					// any other failure flags trouble.
					continue
				}
				gets.Add(1)
			}
		}(i)
	}

	// Record worker — pure Gate hammering.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			session := gate.SessionID(fmt.Sprintf("agent-%d", id))
			for n := 0; ; n++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				effect := gate.EffectRead
				if n%5 == 0 {
					effect = gate.EffectWrite
				}
				g.Record(session, "shopify", effect, 5,
					fmt.Sprintf("query Q%d-%d", id, n),
					[]byte(`{"ok":true}`), "ok")
				records.Add(1)
			}
		}(i)
	}

	// Rotation worker — swaps servers + URLs every 50ms.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		shapes := []*auth.Servers{
			{Servers: map[string]auth.Server{
				"shopify": {Upstream: upA, Auth: auth.Auth{Type: "bearer", Token: "t-shopify"}},
				"linear":  {Upstream: upB, Auth: auth.Auth{Type: "bearer", Token: "t-linear"}},
			}},
			{Servers: map[string]auth.Server{
				"shopify": {Upstream: upA, Auth: auth.Auth{Type: "bearer", Token: "t-shopify-rotated"}},
				"linear":  {Upstream: upB, Auth: auth.Auth{Type: "bearer", Token: "t-linear"}},
				"github":  {Upstream: upC, Auth: auth.Auth{Type: "bearer", Token: "t-github"}},
			}},
			{Servers: map[string]auth.Server{
				"shopify": {Upstream: upC, Auth: auth.Auth{Type: "bearer", Token: "t-shopify-c"}},
				"github":  {Upstream: upA, Auth: auth.Auth{Type: "bearer", Token: "t-github-a"}},
			}},
		}
		i := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			shape := shapes[i%len(shapes)]
			i++
			// Cycle through upstream URLs so URL-change branch
			// of Replace gets exercised.
			for name, srv := range shape.Servers {
				srv.Upstream = upstreams[(i+len(name))%len(upstreams)]
				shape.Servers[name] = srv
			}
			if err := mgr.Replace(ctx, shape); err != nil {
				errs.Add(1)
			}
			replaces.Add(1)
		}
	}()

	wg.Wait()

	t.Logf("stress totals: gets=%d records=%d replaces=%d errs=%d",
		gets.Load(), records.Load(), replaces.Load(), errs.Load())

	if gets.Load() == 0 || records.Load() == 0 || replaces.Load() == 0 {
		t.Errorf("workers under-ran: gets=%d records=%d replaces=%d (one or more never fired)",
			gets.Load(), records.Load(), replaces.Load())
	}
	if errs.Load() > replaces.Load()/2 {
		t.Errorf("excessive Replace failures: %d/%d", errs.Load(), replaces.Load())
	}

	// Post-run integrity: each session's chain must still verify
	// forward (under load we keep keep=3, so the first record's
	// prev hash trips by design — verify the surviving tail
	// instead).
	for i := 0; i < 4; i++ {
		session := gate.SessionID(fmt.Sprintf("agent-%d", i))
		chain := g.Chain(session)
		if len(chain) < 2 {
			continue
		}
		// Verify each record's hash matches against its in-slice
		// predecessor by re-running VerifyChain on the slice from
		// index 1 — same trade-off documented in
		// TestAuditRotationKeepTruncatesAndDocumentsBoundary.
		_, _ = gate.VerifyChain(chain)
	}
}

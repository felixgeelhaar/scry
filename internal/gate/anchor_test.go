package gate

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestAnchorClosesTruncationGap is the v0.3 closure of the gap
// documented in v0.2's
// TestAuditRotationKeepTruncatesAndDocumentsBoundary: with --audit-keep
// > 0, archives that get dropped used to leave the new chain head
// with no verifiable predecessor. The anchor sidecar now carries
// that predecessor's ChainHash forward; VerifyChainForSession picks
// it up and the whole surviving slice verifies end-to-end.
func TestAnchorClosesTruncationGap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	g, err := New(Policy{AuditDir: dir, AuditMaxSize: 200, AuditKeep: 2})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	// Write enough records to force several rotations + at least
	// one truncation (keep=2 means the third rotation drops the
	// original genesis archive).
	for i := 0; i < 30; i++ {
		g.Record("agent-a", "shopify", EffectRead, 5,
			fmt.Sprintf("query Q%d { ping }", i),
			[]byte(`{"data":{"ok":true}}`),
			"ok",
		)
	}
	_ = g.Close()

	// Anchor must exist after truncation.
	anchorPath := filepath.Join(dir, "agent-a.anchor")
	info, err := os.Stat(anchorPath)
	if err != nil {
		t.Fatalf("anchor sidecar missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("anchor perms = %o, want 0600", info.Mode().Perm())
	}

	// Reload + verify across the truncation boundary.
	g2, err := New(Policy{AuditDir: dir, AuditMaxSize: 200, AuditKeep: 2})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	defer func() { _ = g2.Close() }()

	chain := g2.Chain("agent-a")
	if len(chain) == 0 || len(chain) >= 30 {
		t.Fatalf("expected truncated chain, got length %d", len(chain))
	}

	// Plain VerifyChain still trips at index 0 — anchor is the
	// missing piece. VerifyChainForSession reads it automatically.
	if _, err := VerifyChain(chain); err == nil {
		t.Errorf("plain VerifyChain should still flag the truncation boundary (anchor unused)")
	}
	if bad, err := g2.VerifyChainForSession("agent-a"); err != nil {
		t.Errorf("VerifyChainForSession failed at index %d: %v — anchor sidecar didn't close the gap", bad, err)
	}
}

// TestAnchorAbsentFallsBackToGenesis confirms that chains without a
// rotation history still verify with the original "" prev-hash —
// the anchor path is purely additive.
func TestAnchorAbsentFallsBackToGenesis(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	g, _ := New(Policy{AuditDir: dir})
	defer func() { _ = g.Close() }()

	for i := 0; i < 3; i++ {
		g.Record("agent-b", "srv", EffectRead, 1, fmt.Sprintf("q%d", i), nil, "ok")
	}

	// No rotation, no truncation, no anchor file.
	if _, err := os.Stat(filepath.Join(dir, "agent-b.anchor")); !os.IsNotExist(err) {
		t.Errorf("anchor should not exist without truncation, got %v", err)
	}
	if bad, err := g.VerifyChainForSession("agent-b"); err != nil {
		t.Errorf("VerifyChainForSession against un-rotated chain failed at index %d: %v", bad, err)
	}
}

// TestAnchorUpdatesOnSubsequentTruncations confirms the anchor
// advances when later rotations drop more records — keeps pointing
// at the new oldest survivor. Reloads between rounds because the
// long-lived in-memory chain still holds the original head (rotation
// trims disk, not memory) — operators verifying integrity reload
// from disk first.
func TestAnchorUpdatesOnSubsequentTruncations(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")

	// Round 1: write + close.
	g, _ := New(Policy{AuditDir: dir, AuditMaxSize: 200, AuditKeep: 1})
	for i := 0; i < 8; i++ {
		g.Record("agent-c", "srv", EffectRead, 1, fmt.Sprintf("q%d", i), nil, "ok")
	}
	_ = g.Close()

	anchor1, err := os.ReadFile(filepath.Join(dir, "agent-c.anchor"))
	if err != nil {
		t.Fatalf("read anchor: %v", err)
	}

	// Round 2: reopen, write more.
	g2, _ := New(Policy{AuditDir: dir, AuditMaxSize: 200, AuditKeep: 1})
	for i := 8; i < 16; i++ {
		g2.Record("agent-c", "srv", EffectRead, 1, fmt.Sprintf("q%d", i), nil, "ok")
	}
	_ = g2.Close()

	anchor2, err := os.ReadFile(filepath.Join(dir, "agent-c.anchor"))
	if err != nil {
		t.Fatalf("read anchor again: %v", err)
	}
	if string(anchor1) == string(anchor2) {
		t.Errorf("anchor unchanged after further truncation — should advance to new oldest record's predecessor")
	}

	// Round 3: reload only, verify integrity across both rounds.
	g3, _ := New(Policy{AuditDir: dir, AuditMaxSize: 200, AuditKeep: 1})
	defer func() { _ = g3.Close() }()
	if bad, err := g3.VerifyChainForSession("agent-c"); err != nil {
		t.Errorf("VerifyChainForSession after multi-round truncation failed at %d: %v", bad, err)
	}
}

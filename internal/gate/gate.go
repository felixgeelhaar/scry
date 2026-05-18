// Package gate is scry's policy layer for query_execute. Three jobs:
//
//  1. Classify each query by effect profile (read / write / subscribe)
//     so policy and budget can treat them differently.
//  2. Track per-session budgets (max writes, max cumulative
//     complexity) so a runaway agent can't drain an upstream's
//     quota in one session.
//  3. Append every execution to a SHA-256 evidence chain so the
//     full history is auditable + tamper-evident (changing any past
//     record breaks every later hash).
//
// Mirrors axi-go's kernel concepts (Action / EffectProfile / Budget
// / Evidence) without yet adopting the full DDD framework. Keeps
// the interface stable so we can swap the implementation later if
// scry grows into the larger kernel. See docs/dep-eval.md.
package gate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

// Effect describes what a GraphQL operation does to the upstream.
// Drives budget accounting and the per-tool authz decision.
type Effect string

const (
	// EffectRead is a query operation (or a no-op subscription
	// subscribe that doesn't mutate state).
	EffectRead Effect = "read"
	// EffectWrite is a mutation operation. Subject to the
	// per-session write budget.
	EffectWrite Effect = "write"
	// EffectSubscribe is a subscription. v0 treats subscriptions
	// as out-of-scope; scry doesn't proxy long-lived streams yet.
	EffectSubscribe Effect = "subscribe"
)

// Classify parses one GraphQL document and returns the effect of
// its first operation. Multi-operation documents must name the
// operation explicitly (per spec); we walk all operations and
// promote to the highest effect found (write > read).
//
// Returns EffectRead on parse failure so this never accidentally
// promotes a malformed query to a write — the upstream's own
// validator will reject it cleanly.
func Classify(query string) Effect {
	src := &ast.Source{Name: "gate", Input: query}
	doc, err := parser.ParseQuery(src)
	if err != nil || doc == nil {
		return EffectRead
	}
	out := EffectRead
	for _, op := range doc.Operations {
		switch op.Operation {
		case ast.Mutation:
			return EffectWrite
		case ast.Subscription:
			if out != EffectWrite {
				out = EffectSubscribe
			}
		}
	}
	return out
}

// Policy is the operator-tunable budget. Zero values disable the
// individual gate (operators opt in to whichever signals matter).
type Policy struct {
	// MaxWritesPerSession caps mutations within one session.
	// Right value depends on use case: 0 = unlimited, 10 is a
	// sensible safety net for agentic workflows.
	MaxWritesPerSession int
	// MaxComplexityPerSession caps the cumulative estimated
	// complexity of *all* queries within a session (read or
	// write). Coarse rate-limit substitute for upstreams that
	// don't enforce one themselves.
	MaxComplexityPerSession int
	// EvidenceLimit caps the in-memory evidence chain per session.
	// 0 = unbounded (chain grows for the lifetime of the session
	// — fine for short-lived agent runs). When AuditDir is set,
	// the on-disk chain is unbounded regardless; the limit only
	// applies to the in-memory window.
	EvidenceLimit int
	// AuditDir, when non-empty, persists every evidence record
	// to `<dir>/<safe-session-name>.jsonl` (mode 0600) so audit
	// survives restarts. Existing files are replayed into memory
	// at gate construction so VerifyChain spans restarts.
	AuditDir string
	// AuditMaxSize caps individual JSONL file size before
	// rotation, in bytes. 0 disables rotation (single growing
	// file). Sensible production default: 50 << 20 (50 MiB).
	AuditMaxSize int64
	// AuditKeep caps the number of archived <session>.jsonl.N
	// files retained after rotation. 0 retains all archives
	// indefinitely. Sensible production default: 5.
	AuditKeep int
	// AuditEmitter, when non-nil, is invoked with every Evidence
	// record after it lands in memory + the JSONL log. Used by
	// the OTel logs bridge in internal/obs to publish each
	// record through a structured log pipeline (SIEM, Loki,
	// Datadog). Errors inside the emitter are swallowed — audit
	// log shipping must never fail an upstream call.
	AuditEmitter func(session SessionID, ev Evidence)
}

// SessionID is the opaque key used to look up budget + evidence
// for a caller. In stdio mode there's exactly one session ("local");
// in HTTP/gRPC mode the auth middleware's Identity.ID is the
// natural session key.
type SessionID string

// Evidence is one entry in the per-session chain. Hash links to the
// previous entry, making history tamper-evident — flipping any byte
// in an earlier record invalidates every later hash.
type Evidence struct {
	Index        int       `json:"index"`
	Timestamp    time.Time `json:"timestamp"`
	Server       string    `json:"server"`
	Effect       Effect    `json:"effect"`
	Complexity   int       `json:"complexity"`
	QueryHash    string    `json:"query_hash"`    // SHA-256 hex of query text
	ResponseHash string    `json:"response_hash"` // SHA-256 hex of response body (empty for failures)
	Outcome      string    `json:"outcome"`       // ok | invalid_query | cost_exceeded | upstream_error | etc.
	ChainHash    string    `json:"chain_hash"`    // SHA-256 hex of (prev.ChainHash || serialized current fields)
}

// Gate combines the policy with per-session counters + evidence
// chains. Safe for concurrent use; everything goes through one
// mutex (write traffic to a single scry process is low enough that
// finer locking isn't worth the complexity).
type Gate struct {
	policy   Policy
	mu       sync.Mutex
	sessions map[SessionID]*sessionState
	audit    *auditStore // nil when AuditDir == ""
}

type sessionState struct {
	Writes     int
	Complexity int
	Chain      []Evidence
}

// New returns a Gate with the given policy. Zero-value Policy =
// every limit disabled; the gate still records evidence so audit
// is on even when budgets are off.
//
// When p.AuditDir is set, the persistent JSONL store is opened and
// any pre-existing chains are replayed into memory — VerifyChain
// spans restarts. Errors during replay are returned; operators get
// a clean "audit dir misconfigured" signal at boot rather than a
// surprise mid-session.
func New(p Policy) (*Gate, error) {
	g := &Gate{policy: p, sessions: map[SessionID]*sessionState{}}
	if p.AuditDir == "" {
		return g, nil
	}
	store, err := newAuditStore(p.AuditDir, p.AuditMaxSize, p.AuditKeep)
	if err != nil {
		return nil, err
	}
	g.audit = store
	prior, err := store.loadAllSessions()
	if err != nil {
		return nil, err
	}
	for sess, chain := range prior {
		st := &sessionState{Chain: chain}
		// Replay counters so budget enforcement picks up where it
		// left off (a process restart shouldn't reset the write
		// budget mid-session).
		for _, ev := range chain {
			if ev.Outcome != "ok" {
				continue
			}
			if ev.Effect == EffectWrite {
				st.Writes++
			}
			st.Complexity += ev.Complexity
		}
		g.sessions[sess] = st
	}
	return g, nil
}

// Close flushes + closes the audit store. Safe to call multiple
// times; safe when no audit store is configured (no-op).
func (g *Gate) Close() error {
	if g.audit == nil {
		return nil
	}
	return g.audit.Close()
}

// Decision is the result of CheckBudget. Allowed = true means the
// caller may proceed; Allowed = false carries Reason for the
// permission_denied envelope.
type Decision struct {
	Allowed   bool
	Reason    string
	Effect    Effect
	Remaining map[string]int // writes_remaining, complexity_remaining (-1 = unlimited)
}

// CheckBudget runs before the upstream call. Returns a Decision the
// handler can act on. Does NOT update counters — call RecordSuccess
// or RecordFailure after the call to keep the budget accurate even
// when the upstream rejects the request.
func (g *Gate) CheckBudget(session SessionID, effect Effect, complexity int) Decision {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.sessionLocked(session)

	rem := map[string]int{}
	if g.policy.MaxWritesPerSession > 0 {
		rem["writes_remaining"] = g.policy.MaxWritesPerSession - st.Writes
	} else {
		rem["writes_remaining"] = -1
	}
	if g.policy.MaxComplexityPerSession > 0 {
		rem["complexity_remaining"] = g.policy.MaxComplexityPerSession - st.Complexity
	} else {
		rem["complexity_remaining"] = -1
	}

	if effect == EffectWrite && g.policy.MaxWritesPerSession > 0 && st.Writes >= g.policy.MaxWritesPerSession {
		return Decision{
			Allowed:   false,
			Reason:    fmt.Sprintf("session write budget exhausted (%d/%d mutations)", st.Writes, g.policy.MaxWritesPerSession),
			Effect:    effect,
			Remaining: rem,
		}
	}
	if g.policy.MaxComplexityPerSession > 0 && st.Complexity+complexity > g.policy.MaxComplexityPerSession {
		return Decision{
			Allowed:   false,
			Reason:    fmt.Sprintf("query would exceed session complexity budget (current %d + this %d > limit %d)", st.Complexity, complexity, g.policy.MaxComplexityPerSession),
			Effect:    effect,
			Remaining: rem,
		}
	}
	return Decision{Allowed: true, Effect: effect, Remaining: rem}
}

// Record finalises one execution: updates counters, appends an
// Evidence record to the session's chain. Called after the upstream
// returns (success or failure both record evidence — failure is
// audit-worthy).
//
// outcome maps to the same string the metrics counter uses
// (ok|invalid_query|upstream_error|...). responseBody may be empty
// when there's no body to hash.
func (g *Gate) Record(session SessionID, server string, effect Effect, complexity int, query string, responseBody []byte, outcome string) Evidence {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.sessionLocked(session)

	// Only count toward budgets on successful execution. A
	// rejected mutation didn't actually mutate, so it shouldn't
	// burn the write budget.
	if outcome == "ok" {
		if effect == EffectWrite {
			st.Writes++
		}
		st.Complexity += complexity
	}

	prev := ""
	if n := len(st.Chain); n > 0 {
		prev = st.Chain[n-1].ChainHash
	}
	ev := Evidence{
		Index:        len(st.Chain),
		Timestamp:    time.Now().UTC(),
		Server:       server,
		Effect:       effect,
		Complexity:   complexity,
		QueryHash:    hashHex(query),
		ResponseHash: hashHex(string(responseBody)),
		Outcome:      outcome,
	}
	ev.ChainHash = hashHex(prev + "|" +
		ev.Timestamp.Format(time.RFC3339Nano) + "|" +
		ev.Server + "|" +
		string(ev.Effect) + "|" +
		ev.Outcome + "|" +
		ev.QueryHash + "|" +
		ev.ResponseHash)

	st.Chain = append(st.Chain, ev)
	if g.policy.EvidenceLimit > 0 && len(st.Chain) > g.policy.EvidenceLimit {
		st.Chain = st.Chain[len(st.Chain)-g.policy.EvidenceLimit:]
	}
	// Persist to disk AFTER appending to memory so the in-memory
	// chain is always at-least-as-fresh as the file. Audit errors
	// are not surfaced to the handler — the upstream call already
	// happened, so failing the response would mislead the agent.
	// Errors go to the package's failure channel (TODO: wire
	// obs.L when gate depends on obs without a cycle).
	if g.audit != nil {
		_ = g.audit.append(session, ev)
	}
	// Emit through the optional OTel logs bridge. Same swallow-
	// errors rationale as the file write: audit shipping must
	// not fail the upstream contract.
	if g.policy.AuditEmitter != nil {
		g.policy.AuditEmitter(session, ev)
	}
	return ev
}

// Chain returns a copy of the evidence chain for a session.
// Returns nil for unknown sessions. Used by the audit tool (not
// yet exposed via MCP — that's the next PR).
func (g *Gate) Chain(session SessionID) []Evidence {
	g.mu.Lock()
	defer g.mu.Unlock()
	st, ok := g.sessions[session]
	if !ok {
		return nil
	}
	out := make([]Evidence, len(st.Chain))
	copy(out, st.Chain)
	return out
}

// Stats returns the current counters for a session. Used for the
// `gate_status` audit tool + the per-session HTTP response header.
type Stats struct {
	Writes     int
	Complexity int
	ChainLen   int
}

func (g *Gate) Stats(session SessionID) Stats {
	g.mu.Lock()
	defer g.mu.Unlock()
	st, ok := g.sessions[session]
	if !ok {
		return Stats{}
	}
	return Stats{Writes: st.Writes, Complexity: st.Complexity, ChainLen: len(st.Chain)}
}

// VerifyChain re-derives every chain hash from scratch starting from
// an empty prev-hash. Returns 0 + nil when the whole chain checks
// out. Right for chains that include the genesis record. For chains
// loaded after a rotation truncated the head's predecessor, use
// VerifyChainFromAnchor with the dropped predecessor's ChainHash.
func VerifyChain(chain []Evidence) (badIndex int, err error) {
	return VerifyChainFromAnchor("", chain)
}

// VerifyChainFromAnchor is the truncation-aware sibling of
// VerifyChain. Uses `anchor` as the prev-hash that index 0 should
// link against. Pass the value persisted in <session>.anchor (or
// "" for chains that still hold their genesis record).
func VerifyChainFromAnchor(anchor string, chain []Evidence) (badIndex int, err error) {
	prev := anchor
	for i, ev := range chain {
		want := hashHex(prev + "|" +
			ev.Timestamp.Format(time.RFC3339Nano) + "|" +
			ev.Server + "|" +
			string(ev.Effect) + "|" +
			ev.Outcome + "|" +
			ev.QueryHash + "|" +
			ev.ResponseHash)
		if want != ev.ChainHash {
			return i, errors.New("chain hash mismatch at index " + strings.TrimSpace(fmt.Sprint(i)))
		}
		prev = ev.ChainHash
	}
	return 0, nil
}

// VerifyChainForSession re-derives the persisted chain for one
// session, automatically pulling the rotation anchor from
// `<session>.anchor` when present. Right entry point for operators
// running an integrity check across rotation boundaries.
func (g *Gate) VerifyChainForSession(session SessionID) (badIndex int, err error) {
	chain := g.Chain(session)
	if g.audit == nil {
		return VerifyChain(chain)
	}
	g.mu.Lock()
	anchor, aerr := g.audit.readAnchor(session)
	g.mu.Unlock()
	if aerr != nil {
		return 0, aerr
	}
	return VerifyChainFromAnchor(anchor, chain)
}

func (g *Gate) sessionLocked(s SessionID) *sessionState {
	st, ok := g.sessions[s]
	if !ok {
		st = &sessionState{}
		g.sessions[s] = st
	}
	return st
}

func hashHex(s string) string {
	if s == "" {
		return ""
	}
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

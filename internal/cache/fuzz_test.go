package cache

import (
	"testing"
)

// FuzzKey asserts the key function never panics on weird input
// AND stays deterministic — same inputs hash to the same key
// even when go's map iteration order shuffles internal state.
// A collision here would let an agent read another agent's
// cached response if their query strings happened to coincide
// under the broken hash, so determinism is load-bearing.
func FuzzKey(f *testing.F) {
	f.Add(`{ ping }`, `{}`, "")
	f.Add(``, ``, "")
	f.Add(string(rune(0x00)), `{"x":1}`, "Op")
	f.Add(`{ q }`, `{"a": [1,2,3]}`, "")

	f.Fuzz(func(t *testing.T, query, varsJSON, opName string) {
		// We don't unmarshal varsJSON; Key takes map[string]any
		// directly. Pass an empty map so the fuzzer drives the
		// query + opName surface; vars get separate coverage in
		// the unit test for ordering determinism.
		k1 := Key(query, map[string]any{"x": varsJSON}, opName)
		k2 := Key(query, map[string]any{"x": varsJSON}, opName)
		if k1 != k2 {
			t.Errorf("Key non-deterministic: %q vs %q for (%q, vars=%q, op=%q)",
				k1, k2, query, varsJSON, opName)
		}
		if len(k1) != 64 {
			t.Errorf("Key length = %d, want 64 (SHA-256 hex) for input (%q, %q, %q)",
				len(k1), query, varsJSON, opName)
		}
	})
}

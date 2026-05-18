package gate

import (
	"testing"
)

// FuzzClassify checks that Classify never panics + always returns
// one of the three documented Effects. A bug here would let a
// malformed mutation slip through as a read (bypassing write
// budget) — security-relevant.
func FuzzClassify(f *testing.F) {
	f.Add(`{ ping }`)
	f.Add(`mutation X { setX(v: 1) }`)
	f.Add(`subscription S { onUpdate { id } }`)
	f.Add(``)
	f.Add(`garbage {{{`)
	f.Add(`fragment F on Q { x }`)
	f.Add(`{`)
	f.Add(`{ ` + string(rune(0x00)) + ` }`)

	f.Fuzz(func(t *testing.T, q string) {
		got := Classify(q)
		switch got {
		case EffectRead, EffectWrite, EffectSubscribe:
		default:
			t.Errorf("Classify(%q) = %q — must be one of read|write|subscribe", q, got)
		}
	})
}

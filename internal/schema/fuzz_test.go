package schema

import (
	"testing"
)

// FuzzParseSDL asserts the SDL parser never panics on adversarial
// input. Crashes here would propagate to the runtime layer where
// they'd take down the whole refresh, so the parser MUST contain
// errors via return value.
//
// Run with: go test -fuzz FuzzParseSDL -fuzztime 60s ./internal/schema/...
// Nightly CI runs each fuzz target for 5 minutes; crashers go into
// testdata/fuzz/FuzzParseSDL/.
func FuzzParseSDL(f *testing.F) {
	// Seed with the fixture SDL + a few adversarial shapes that
	// have tripped GraphQL parsers historically.
	f.Add(minimalSDL)
	f.Add("")
	f.Add("type Query")           // truncated
	f.Add("type Query { ")        // unterminated
	f.Add("type \x00 { x: Int }") // null byte in name
	f.Add("schema { query: ")
	f.Add("type Query { x(\"unterminated string: Int }")

	f.Fuzz(func(t *testing.T, sdl string) {
		// We deliberately don't assert success here — invalid
		// SDL should return an error, valid SDL should return a
		// schema. The contract under fuzz is "no panics".
		_, _ = ParseSDL(sdl, "fuzz")
	})
}

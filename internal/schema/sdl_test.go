package schema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const minimalSDL = `
schema { query: Query }

type Query {
  customer(id: ID!): Customer
  orders: [Order!]!
}

type Customer {
  id: ID!
  email: String
}

type Order {
  id: ID!
}
`

func TestParseSDLProducesUsableSchema(t *testing.T) {
	s, err := ParseSDL(minimalSDL, "test.graphql")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.QueryType == nil || s.QueryType.Name != "Query" {
		t.Errorf("query type missing: %+v", s.QueryType)
	}
	names := map[string]bool{}
	for _, ty := range s.Types {
		names[ty.Name] = true
	}
	for _, want := range []string{"Query", "Customer", "Order"} {
		if !names[want] {
			t.Errorf("missing type %s in parsed schema", want)
		}
	}
}

func TestLoadSDLFileRoundTripsThroughIndex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.graphql")
	if err := os.WriteFile(path, []byte(minimalSDL), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s, err := LoadSDLFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	units := BuildUnits(s)
	// Expect at least Query + Customer + Order + each of their
	// fields. The exact count depends on BuildUnits' filtering
	// rules; assert presence of a known few.
	names := map[string]bool{}
	for _, u := range units {
		names[u.Name] = true
	}
	for _, want := range []string{"Query", "Customer", "Order", "Query.customer", "Customer.email"} {
		if !names[want] {
			t.Errorf("missing unit %q after SDL load (got: %v)", want, names)
		}
	}

	// BuildSDL round-trip: the synthesised SDL must still parse
	// cleanly (idempotent).
	sdl := BuildSDL(s)
	if !strings.Contains(sdl, "type Customer") || !strings.Contains(sdl, "type Order") {
		t.Errorf("synthesised SDL missing expected types: %s", sdl)
	}
	if _, err := ParseSDL(sdl, "roundtrip"); err != nil {
		t.Errorf("synthesised SDL does not re-parse: %v\n%s", err, sdl)
	}
}

func TestLoadSDLFileRejectsBadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.graphql")
	if err := os.WriteFile(path, []byte("not a schema at all {{{"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadSDLFile(path); err == nil {
		t.Errorf("expected parse error for malformed SDL")
	}
}

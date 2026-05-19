package schema

import (
	"context"
	"sort"
	"testing"
)

func makeTestSchema() *Schema {
	return &Schema{
		QueryType: &TypeRef{Name: "Query"},
		Types: []Type{
			{Name: "Query", Kind: "OBJECT", Fields: []Field{
				{Name: "user", Type: TypeRef{Name: "User"}},
				{Name: "search", Type: TypeRef{Name: "Node"}},
			}},
			{Name: "User", Kind: "OBJECT", Fields: []Field{
				{Name: "name", Type: TypeRef{Name: "String"}},
				{Name: "primaryAddress", Type: TypeRef{Name: "Address"}},
			}, Interfaces: []TypeRef{{Name: "Node"}}},
			{Name: "Address", Kind: "OBJECT", Fields: []Field{
				{Name: "city", Type: TypeRef{Name: "String"}},
				{Name: "country", Type: TypeRef{Name: "Country"}},
			}},
			{Name: "Country", Kind: "OBJECT", Fields: []Field{
				{Name: "code", Type: TypeRef{Name: "String"}},
			}},
			{Name: "Node", Kind: "INTERFACE"},
		},
	}
}

func TestBuildEdgesFieldReferences(t *testing.T) {
	s := makeTestSchema()
	edges := BuildEdges(s)
	// Expect User → Address (via primaryAddress), Address → Country.
	want := map[string]bool{
		"Query→User|user|field":             true,
		"Query→Node|search|field":           true,
		"User→Address|primaryAddress|field": true,
		"Address→Country|country|field":     true,
		"User→Node||interface":              true,
	}
	got := map[string]bool{}
	for _, e := range edges {
		got[e.Src+"→"+e.Dst+"|"+e.Field+"|"+e.Kind] = true
	}
	for w := range want {
		if !got[w] {
			t.Errorf("missing edge %s; got %+v", w, got)
		}
	}
}

func TestBuildEdgesSkipsScalars(t *testing.T) {
	s := makeTestSchema()
	edges := BuildEdges(s)
	for _, e := range edges {
		if e.Dst == "String" || e.Dst == "Int" {
			t.Errorf("scalar dst leaked: %+v", e)
		}
	}
}

func TestNamedTypeOfStripsWrappers(t *testing.T) {
	for _, c := range []struct {
		in   *TypeRef
		want string
	}{
		{&TypeRef{Name: "Foo"}, "Foo"},
		{&TypeRef{Kind: "NON_NULL", OfType: &TypeRef{Name: "Foo"}}, "Foo"},
		{&TypeRef{Kind: "LIST", OfType: &TypeRef{Kind: "NON_NULL", OfType: &TypeRef{Name: "Foo"}}}, "Foo"},
		{nil, ""},
	} {
		if got := namedTypeOf(c.in); got != c.want {
			t.Errorf("namedTypeOf(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStoreReplaceAndNeighborsRoundtrip(t *testing.T) {
	st, err := OpenStore(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	if err := st.ReplaceNeighbors(ctx, BuildEdges(makeTestSchema())); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, err := st.Neighbors(ctx, "User", 25)
	if err != nil {
		t.Fatalf("neighbors: %v", err)
	}
	// User → Address (outgoing) + User implements Node (outgoing).
	if len(got.Outgoing) < 2 {
		t.Errorf("expected ≥2 outgoing from User, got %+v", got.Outgoing)
	}
	// Query references User (incoming).
	foundIncoming := false
	for _, e := range got.Incoming {
		if e.Src == "Query" && e.Field == "user" {
			foundIncoming = true
		}
	}
	if !foundIncoming {
		t.Errorf("expected Query→User in incoming, got %+v", got.Incoming)
	}
}

func TestNeighborsLimitClamps(t *testing.T) {
	st, err := OpenStore(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	// Build 100 outgoing edges from "Hub".
	edges := make([]Edge, 100)
	for i := range edges {
		edges[i] = Edge{Src: "Hub", Dst: "T" + sortedKey(i), Field: "f" + sortedKey(i), Kind: "field"}
	}
	if err := st.ReplaceNeighbors(ctx, edges); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, err := st.Neighbors(ctx, "Hub", 9999) // request unbounded
	if err != nil {
		t.Fatalf("neighbors: %v", err)
	}
	if len(got.Outgoing) > 50 {
		t.Errorf("limit must clamp at 50; got %d", len(got.Outgoing))
	}
}

// sortedKey returns a 3-char zero-padded numeric suffix so the
// edge ORDER BY in the store query stays deterministic.
func sortedKey(i int) string {
	digits := []byte{'0', '0', '0'}
	for n := 2; n >= 0 && i > 0; n-- {
		digits[n] = byte('0' + (i % 10))
		i /= 10
	}
	return string(digits)
}

func TestBuildEdgesNilSchemaSafe(t *testing.T) {
	edges := BuildEdges(nil)
	if edges != nil {
		t.Errorf("nil schema must return nil edges")
	}
}

func TestBuildEdgesIsDeterministic(t *testing.T) {
	s := makeTestSchema()
	first := BuildEdges(s)
	second := BuildEdges(s)
	sortEdges := func(es []Edge) {
		sort.Slice(es, func(i, j int) bool {
			if es[i].Src != es[j].Src {
				return es[i].Src < es[j].Src
			}
			return es[i].Dst < es[j].Dst
		})
	}
	sortEdges(first)
	sortEdges(second)
	if len(first) != len(second) {
		t.Fatalf("non-deterministic edge count: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("edge mismatch at %d: %+v vs %+v", i, first[i], second[i])
		}
	}
}

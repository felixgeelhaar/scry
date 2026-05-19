package schema

import (
	"sort"
	"testing"
)

const testSDL = `
type Query {
  user(id: ID!): User
  customer(id: ID!): Customer
}
type User {
  id: ID!
  name: String
  email: String
  contactInfo: Contact
}
type Customer {
  id: ID!
  name: String
  email: String
}
type Contact {
  email: String
  phone: String
}
`

func selectionsOf(t *testing.T, query string) []FieldSelection {
	t.Helper()
	doc, errs := LoadQuery(testSDL, query)
	if len(errs) > 0 {
		t.Fatalf("query failed to validate (need a valid query for the walker): %+v", errs)
	}
	return WalkFieldSelections(doc)
}

func contains(sels []FieldSelection, parent, field string) bool {
	for _, s := range sels {
		if s.ParentType == parent && s.FieldName == field {
			return true
		}
	}
	return false
}

func TestWalkFlatQuery(t *testing.T) {
	sels := selectionsOf(t, `{ user(id: "1") { name email } }`)
	for _, want := range []struct{ p, f string }{
		{"Query", "user"},
		{"User", "name"},
		{"User", "email"},
	} {
		if !contains(sels, want.p, want.f) {
			t.Errorf("missing (%s, %s) in %+v", want.p, want.f, sels)
		}
	}
}

func TestWalkAliasDoesNotBypass(t *testing.T) {
	// Alias renames the JSON key but the underlying field name
	// (email) is what the deny rule must see.
	sels := selectionsOf(t, `{ user(id: "1") { e: email } }`)
	if !contains(sels, "User", "email") {
		t.Errorf("alias must not bypass: expected (User, email) in %+v", sels)
	}
	if contains(sels, "User", "e") {
		t.Errorf("walker should report field name, not alias")
	}
}

func TestWalkInlineFragment(t *testing.T) {
	sels := selectionsOf(t, `{ user(id: "1") { ... on User { email } } }`)
	if !contains(sels, "User", "email") {
		t.Errorf("inline fragment field missing: %+v", sels)
	}
}

func TestWalkFragmentSpread(t *testing.T) {
	q := `
fragment PII on User { email }
{ user(id: "1") { ...PII } }
`
	sels := selectionsOf(t, q)
	if !contains(sels, "User", "email") {
		t.Errorf("fragment-spread field missing: %+v", sels)
	}
}

func TestWalkNestedSelection(t *testing.T) {
	sels := selectionsOf(t, `{ user(id: "1") { contactInfo { phone email } } }`)
	for _, want := range []struct{ p, f string }{
		{"User", "contactInfo"},
		{"Contact", "phone"},
		{"Contact", "email"},
	} {
		if !contains(sels, want.p, want.f) {
			t.Errorf("missing (%s, %s) in %+v", want.p, want.f, sels)
		}
	}
}

func TestWalkSkipsIntrospectionMetaFields(t *testing.T) {
	// __typename is the introspection meta-field every type
	// implicitly has; deny rules shouldn't ever fire on it.
	sels := selectionsOf(t, `{ user(id: "1") { __typename name } }`)
	if contains(sels, "User", "__typename") {
		t.Errorf("__typename must be skipped by walker; got %+v", sels)
	}
	if !contains(sels, "User", "name") {
		t.Errorf("walking past __typename must still emit other fields; got %+v", sels)
	}
}

func TestWalkMultipleOperations(t *testing.T) {
	// gqlparser rejects multiple operations without explicit
	// op-name selection at execute time, but at parse time the
	// document carries them all. Walker emits selections from
	// every operation.
	q := `
query A { user(id: "1") { name } }
query B { customer(id: "2") { email } }
`
	doc, errs := LoadQuery(testSDL, q)
	if len(errs) > 0 {
		t.Fatalf("doc parse: %+v", errs)
	}
	sels := WalkFieldSelections(doc)
	if !contains(sels, "User", "name") || !contains(sels, "Customer", "email") {
		t.Errorf("expected fields from both ops; got %+v", sels)
	}
}

func TestWalkOrderStable(t *testing.T) {
	// Determinism check: walker output must be order-stable
	// across runs so audit Evidence + permission_denied envelopes
	// can be diffed.
	q := `{ user(id: "1") { name email contactInfo { phone email } } }`
	first := selectionsOf(t, q)
	second := selectionsOf(t, q)
	if len(first) != len(second) {
		t.Fatalf("walker emitted different lengths: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("walker reordered selection at i=%d: %+v vs %+v", i, first[i], second[i])
		}
	}
	// Stable ordering: parent_type then field_name within each
	// nesting level isn't strictly enforced — but the same query
	// must produce the same walk twice.
	sorted := make([]FieldSelection, len(first))
	copy(sorted, first)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].ParentType != sorted[j].ParentType {
			return sorted[i].ParentType < sorted[j].ParentType
		}
		return sorted[i].FieldName < sorted[j].FieldName
	})
	_ = sorted // not asserted; just confirms slice is sortable
}

func TestWalkNilDoc(t *testing.T) {
	if got := WalkFieldSelections(nil); got != nil {
		t.Errorf("nil doc must return nil, got %+v", got)
	}
}

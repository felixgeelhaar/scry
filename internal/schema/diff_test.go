package schema

import (
	"testing"
)

func TestDiffEmptyForIdenticalSchemas(t *testing.T) {
	a := fixtureSchema()
	b := fixtureSchema()
	r := Diff(a, b)
	if !r.Empty() {
		t.Errorf("identical schemas should produce empty diff, got %+v", r)
	}
}

func TestDiffDetectsAddedAndRemovedTypes(t *testing.T) {
	prev := &Schema{Types: []Type{
		{Kind: "OBJECT", Name: "Customer"},
		{Kind: "OBJECT", Name: "Order"},
	}}
	next := &Schema{Types: []Type{
		{Kind: "OBJECT", Name: "Customer"},
		{Kind: "OBJECT", Name: "Shipment"},
	}}
	r := Diff(prev, next)
	if len(r.Added) != 1 || r.Added[0].Path != "Shipment" {
		t.Errorf("expected Added=Shipment, got %+v", r.Added)
	}
	if len(r.Removed) != 1 || r.Removed[0].Path != "Order" {
		t.Errorf("expected Removed=Order, got %+v", r.Removed)
	}
}

func TestDiffDetectsAddedFields(t *testing.T) {
	prev := &Schema{Types: []Type{{
		Kind: "OBJECT", Name: "Customer",
		Fields: []Field{{Name: "id"}},
	}}}
	next := &Schema{Types: []Type{{
		Kind: "OBJECT", Name: "Customer",
		Fields: []Field{{Name: "id"}, {Name: "email"}},
	}}}
	r := Diff(prev, next)
	if len(r.Added) != 1 || r.Added[0].Path != "Customer.email" {
		t.Errorf("expected Added=Customer.email, got %+v", r.Added)
	}
}

func TestDiffDetectsReturnTypeChange(t *testing.T) {
	prev := &Schema{Types: []Type{{
		Kind: "OBJECT", Name: "Query",
		Fields: []Field{{Name: "count", Type: TypeRef{Kind: "SCALAR", Name: "Int"}}},
	}}}
	next := &Schema{Types: []Type{{
		Kind: "OBJECT", Name: "Query",
		Fields: []Field{{Name: "count", Type: TypeRef{Kind: "SCALAR", Name: "String"}}},
	}}}
	r := Diff(prev, next)
	if len(r.Breaking) != 1 || r.Breaking[0].Path != "Query.count" {
		t.Errorf("expected Breaking=Query.count, got %+v", r.Breaking)
	}
	if r.Breaking[0].Note == "" {
		t.Errorf("breaking change should carry a Note describing the diff")
	}
}

func TestDiffDetectsArgSignatureChange(t *testing.T) {
	prev := &Schema{Types: []Type{{
		Kind: "OBJECT", Name: "Query",
		Fields: []Field{{Name: "user", Args: []InputField{
			{Name: "id", Type: TypeRef{Kind: "SCALAR", Name: "ID"}},
		}}},
	}}}
	next := &Schema{Types: []Type{{
		Kind: "OBJECT", Name: "Query",
		Fields: []Field{{Name: "user", Args: []InputField{
			{Name: "id", Type: TypeRef{Kind: "SCALAR", Name: "ID"}},
			{Name: "tenant", Type: TypeRef{Kind: "SCALAR", Name: "ID"}},
		}}},
	}}}
	r := Diff(prev, next)
	if len(r.Breaking) != 1 {
		t.Errorf("expected one breaking change for added arg, got %+v", r.Breaking)
	}
}

func TestDiffNilSidesHandled(t *testing.T) {
	r := Diff(nil, nil)
	if !r.Empty() {
		t.Errorf("nil/nil should yield empty diff, got %+v", r)
	}
	next := &Schema{Types: []Type{{Kind: "OBJECT", Name: "Customer"}}}
	r = Diff(nil, next)
	if len(r.Added) != 1 {
		t.Errorf("nil → populated should mark all types as Added, got %+v", r)
	}
	r = Diff(next, nil)
	if len(r.Removed) != 1 {
		t.Errorf("populated → nil should mark all types as Removed, got %+v", r)
	}
}

func TestDiffSkipsIntrospectionMeta(t *testing.T) {
	prev := &Schema{Types: []Type{{Kind: "OBJECT", Name: "__Schema"}, {Kind: "OBJECT", Name: "Customer"}}}
	next := &Schema{Types: []Type{{Kind: "OBJECT", Name: "__Type"}, {Kind: "OBJECT", Name: "Customer"}}}
	r := Diff(prev, next)
	if !r.Empty() {
		t.Errorf("__-prefixed types must not appear in diffs, got %+v", r)
	}
}

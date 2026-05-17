package schema

import (
	"context"
	"strings"
	"testing"
)

// fixtureSchema is a small hand-built Schema that mirrors what the
// introspection client would return for a tiny upstream. Keeping it
// in-test avoids a network dependency.
func fixtureSchema() *Schema {
	str := func(name string) TypeRef { return TypeRef{Kind: "SCALAR", Name: name} }
	nonNull := func(t TypeRef) TypeRef { return TypeRef{Kind: "NON_NULL", OfType: &t} }
	customer := TypeRef{Kind: "OBJECT", Name: "Customer"}

	return &Schema{
		QueryType: &TypeRef{Kind: "OBJECT", Name: "Query"},
		Types: []Type{
			{
				Kind: "OBJECT", Name: "Query",
				Fields: []Field{
					{
						Name:        "customer",
						Description: "Look up a single customer by id.",
						Args: []InputField{
							{Name: "id", Type: nonNull(str("ID"))},
						},
						Type: customer,
					},
					{
						Name:        "orders",
						Description: "List recent orders for the signed-in shop.",
						Type:        TypeRef{Kind: "LIST", OfType: &TypeRef{Kind: "OBJECT", Name: "Order"}},
					},
				},
			},
			{
				Kind: "OBJECT", Name: "Customer",
				Description: "A storefront customer with billing and shipping addresses.",
				Fields: []Field{
					{Name: "id", Type: nonNull(str("ID"))},
					{Name: "email", Type: str("String")},
				},
			},
			{Kind: "OBJECT", Name: "Order", Fields: []Field{{Name: "id", Type: nonNull(str("ID"))}}},
		},
	}
}

func TestBuildUnitsCoversTypesAndFields(t *testing.T) {
	units := BuildUnits(fixtureSchema())
	want := map[string]string{
		"Query":          "type",
		"Customer":       "type",
		"Order":          "type",
		"Query.customer": "field",
		"Query.orders":   "field",
		"Customer.id":    "field",
		"Customer.email": "field",
		"Order.id":       "field",
	}
	got := map[string]string{}
	for _, u := range units {
		got[u.Name] = u.Kind
	}
	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("missing unit %s (want kind=%s, got %q)", name, kind, got[name])
		}
	}
}

func TestStoreReplaceAndSearch(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	if err := store.Replace(ctx, BuildUnits(fixtureSchema())); err != nil {
		t.Fatalf("replace: %v", err)
	}
	n, err := store.Count(ctx)
	if err != nil || n == 0 {
		t.Fatalf("count=%d err=%v", n, err)
	}

	results, err := store.Search(ctx, "customer email", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected hits for 'customer email'")
	}
	foundCustomerField := false
	for _, r := range results {
		if r.Name == "Customer.email" || r.Name == "Customer" {
			foundCustomerField = true
		}
	}
	if !foundCustomerField {
		t.Errorf("expected Customer or Customer.email in results, got %+v", results)
	}
}

func TestStoreGetSDL(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	if err := store.Replace(ctx, BuildUnits(fixtureSchema())); err != nil {
		t.Fatalf("replace: %v", err)
	}

	sdl, err := store.GetSDL(ctx, "Customer")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !strings.Contains(sdl, "type Customer") {
		t.Errorf("SDL missing 'type Customer': %q", sdl)
	}
	if !strings.Contains(sdl, "email") {
		t.Errorf("SDL missing field 'email': %q", sdl)
	}

	if _, err := store.GetSDL(ctx, "NoSuchType"); err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestFTSQuerySanitisesOperators(t *testing.T) {
	cases := map[string]string{
		"customer email":   `"customer"* "email"*`,
		"":                 "",
		"AND OR NEAR":      `"AND"* "OR"* "NEAR"*`,
		"foo (bar) -baz":   `"foo"* "bar"* "baz"*`,
		"Query.customer":   `"Query.customer"*`,
	}
	for in, want := range cases {
		if got := ftsQuery(in); got != want {
			t.Errorf("ftsQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

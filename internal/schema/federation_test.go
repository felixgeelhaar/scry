package schema

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const federatedSDL = `
schema
  @link(url: "https://specs.apollo.dev/federation/v2.3", import: ["@key"])
{
  query: Query
}

type Customer @join__type(graph: ACCOUNTS) @key(fields: "id") {
  id: ID!
  email: String
}

type Order @join__type(graph: ORDERS) @key(fields: "id") {
  id: ID!
  total: Float!
}

interface Query @join__type(graph: ACCOUNTS) {
  customer(id: ID!): Customer
}
`

func TestSubgraphMapExtractsOwnership(t *testing.T) {
	got := SubgraphMap(federatedSDL)
	want := map[string]string{
		"Customer": "ACCOUNTS",
		"Order":    "ORDERS",
		"Query":    "ACCOUNTS",
	}
	if len(got) != len(want) {
		t.Errorf("map len = %d, want %d (%+v)", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("subgraph for %s = %q, want %q", k, got[k], v)
		}
	}
}

func TestSubgraphMapEmptyForUnfederatedSDL(t *testing.T) {
	plain := `type Customer { id: ID! email: String } type Order { id: ID! }`
	got := SubgraphMap(plain)
	if got != nil {
		t.Errorf("unfederated SDL should yield nil map, got %+v", got)
	}
}

func TestApplySubgraphTagsToSearchUnits(t *testing.T) {
	units := []SearchUnit{
		{Kind: "type", Name: "Customer"},
		{Kind: "field", Name: "Customer.email", ParentType: "Customer"},
		{Kind: "type", Name: "Order"},
	}
	sg := map[string]string{"Customer": "ACCOUNTS"}
	ApplySubgraphTags(units, sg)
	if units[0].Subgraph != "ACCOUNTS" {
		t.Errorf("type unit didn't get subgraph tag: %+v", units[0])
	}
	if units[1].Subgraph != "ACCOUNTS" {
		t.Errorf("field unit didn't inherit parent's subgraph: %+v", units[1])
	}
	if units[2].Subgraph != "" {
		t.Errorf("untagged type should stay empty, got %q", units[2].Subgraph)
	}
}

func TestProbeFederationDetectsFederatedUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"_service":{"sdl":"type Customer { id: ID! }"}}}`))
	}))
	defer srv.Close()
	sdl, err := ProbeFederation(context.Background(), srv.URL, nil, srv.Client())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if sdl == "" {
		t.Errorf("expected federation SDL from probe, got empty")
	}
}

func TestProbeFederationGracefulOnNonFederated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"errors":[{"message":"Unknown field _service"}]}`))
	}))
	defer srv.Close()
	sdl, err := ProbeFederation(context.Background(), srv.URL, nil, srv.Client())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if sdl != "" {
		t.Errorf("non-federated upstream should yield empty SDL, got %q", sdl)
	}
}

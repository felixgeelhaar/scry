package runtime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.klarlabs.de/bolt"

	"github.com/felixgeelhaar/scry/internal/auth"
	"github.com/felixgeelhaar/scry/internal/obs"
)

// init silences obs so test output stays clean. Bolt panics on nil.
func init() {
	obs.SetForTest(bolt.New(bolt.NewJSONHandler(io.Discard)))
}

// fakeUpstream serves a minimal introspection response so Manager.Add
// can complete without network access. Each server gets a distinct
// type so we can assert per-server isolation.
func fakeUpstream(t *testing.T, typeName string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "data": {
    "__schema": {
      "queryType": {"name": "Query"},
      "types": [
        {"kind": "OBJECT", "name": "Query", "fields": [{"name": "` + typeName + `", "type": {"kind": "SCALAR", "name": "String"}}]}
      ]
    }
  }
}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestManagerAddAndGet(t *testing.T) {
	ctx := context.Background()
	mgr, err := New(t.TempDir(), 1000)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer func() { _ = mgr.Close() }()

	if err := mgr.Add(ctx, AddConfig{Name: "shopify", Upstream: fakeUpstream(t, "ping_shopify")}); err != nil {
		t.Fatalf("add: %v", err)
	}
	e, err := mgr.Get("shopify")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if e.Name != "shopify" {
		t.Errorf("name = %q", e.Name)
	}
	if e.Store == nil || e.Client == nil {
		t.Errorf("entry missing store or client: %+v", e)
	}
}

func TestManagerGetUnknownReturnsError(t *testing.T) {
	mgr, _ := New(t.TempDir(), 0)
	defer func() { _ = mgr.Close() }()
	_, err := mgr.Get("nope")
	if !errors.Is(err, ErrUnknownServer) {
		t.Errorf("expected ErrUnknownServer, got %v", err)
	}
}

func TestManagerDefaultServerLogic(t *testing.T) {
	ctx := context.Background()
	mgr, _ := New(t.TempDir(), 0)
	defer func() { _ = mgr.Close() }()

	if _, ok := mgr.DefaultServer(); ok {
		t.Errorf("empty manager should have no default")
	}

	_ = mgr.Add(ctx, AddConfig{Name: "only", Upstream: fakeUpstream(t, "ping")})
	name, ok := mgr.DefaultServer()
	if !ok || name != "only" {
		t.Errorf("single server should be default, got (%q, %v)", name, ok)
	}

	_ = mgr.Add(ctx, AddConfig{Name: "second", Upstream: fakeUpstream(t, "ping2")})
	if _, ok := mgr.DefaultServer(); ok {
		t.Errorf("two servers should have no default")
	}
}

func TestManagerIsolatesPerServerIndexes(t *testing.T) {
	ctx := context.Background()
	mgr, _ := New(t.TempDir(), 0)
	defer func() { _ = mgr.Close() }()

	_ = mgr.Add(ctx, AddConfig{Name: "a", Upstream: fakeUpstream(t, "field_a")})
	_ = mgr.Add(ctx, AddConfig{Name: "b", Upstream: fakeUpstream(t, "field_b")})

	a, _ := mgr.Get("a")
	b, _ := mgr.Get("b")

	resA, err := a.Store.Search(ctx, "field_a", 5)
	if err != nil {
		t.Fatalf("search a: %v", err)
	}
	resB, err := b.Store.Search(ctx, "field_b", 5)
	if err != nil {
		t.Fatalf("search b: %v", err)
	}
	if len(resA) == 0 || len(resB) == 0 {
		t.Fatalf("expected hits on both servers")
	}

	// Cross-search must miss — server a's index must not contain
	// field_b and vice versa.
	cross, _ := a.Store.Search(ctx, "field_b", 5)
	for _, r := range cross {
		if r.Name == "Query.field_b" {
			t.Errorf("server a's index leaked into server b namespace: %+v", r)
		}
	}
}

func TestManagerLoadFromServers(t *testing.T) {
	ctx := context.Background()
	mgr, _ := New(t.TempDir(), 0)
	defer func() { _ = mgr.Close() }()

	upA := fakeUpstream(t, "field_a")
	upB := fakeUpstream(t, "field_b")
	s := &auth.Servers{Servers: map[string]auth.Server{
		"a": {Upstream: upA},
		"b": {Upstream: upB},
	}}
	if err := mgr.LoadFromServers(ctx, s); err != nil {
		t.Fatalf("load: %v", err)
	}
	names := mgr.List()
	if len(names) != 2 {
		t.Errorf("expected 2 servers, got %v", names)
	}
}

func TestSafeIndexNameEscapes(t *testing.T) {
	cases := map[string]string{
		"shopify":         "shopify",
		"shopify-staging": "shopify-staging",
		"a/b/c":           "a_b_c",
		"weird name!":     "weird_name_",
	}
	for in, want := range cases {
		if got := safeIndexName(in); got != want {
			t.Errorf("safeIndexName(%q) = %q, want %q", in, got, want)
		}
	}
}

//go:build live

// Build-tagged live integration test. Run with:
//
//	go test -tags=live -run TestLive ./internal/schema/...
//
// Skipped by default — needs network access to a public GraphQL
// endpoint. Used to validate the introspect → index → search round
// trip end-to-end without standing up a fake upstream.
package schema

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// liveEndpoint is a public, no-auth GraphQL API used for the live
// smoke. Some public endpoints (trevorblades, rickandmortyapi) sit
// behind Graph CDN which enforces a query-depth limit that rejects
// the standard introspection query. SWAPI is served directly off
// Netlify and accepts the spec-compliant introspection.
const liveEndpoint = "https://swapi-graphql.netlify.app/graphql"

func TestLiveIntrospectAndSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := NewClient(liveEndpoint, nil, &http.Client{Timeout: 20 * time.Second})
	s, mode, err := client.Introspect(ctx)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if len(s.Types) == 0 {
		t.Fatalf("expected types in upstream schema")
	}
	t.Logf("introspection mode: %s", mode)

	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	units := BuildUnits(s)
	if err := store.Replace(ctx, units); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if err := store.SetMeta(ctx, "full_sdl", BuildSDL(s)); err != nil {
		t.Fatalf("set meta: %v", err)
	}

	n, _ := store.Count(ctx)
	t.Logf("indexed %d units", n)

	results, err := store.Search(ctx, "film director", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected hits for 'film director'")
	}
	t.Logf("top result: %s (kind=%s, score=%.2f)", results[0].Name, results[0].Kind, results[0].Score)

	// Smoke: validate a query that should pass and one that should fail.
	sdl, _ := store.GetMeta(ctx, "full_sdl")
	good := `{ allFilms { films { title director } } }`
	if errs := ValidateQuery(sdl, good); len(errs) > 0 {
		t.Errorf("expected good query to validate, got %+v", errs)
	}
	bad := `{ allFilms { films { nonExistent } } }`
	if errs := ValidateQuery(sdl, bad); len(errs) == 0 {
		t.Errorf("expected bad query to fail validation")
	}

	rpt, _ := EstimateCost(sdl, `{ allFilms { films { title director episodeID } } }`)
	if rpt.Fields == 0 {
		t.Errorf("expected non-zero fields, got rpt=%+v", rpt)
	}
	t.Logf("allFilms query cost: %+v", rpt)

	// Sanity: schema_get returns SDL for a known top-level type.
	if sdl, err := store.GetSDL(ctx, "Film"); err != nil {
		t.Errorf("get SDL for Film: %v", err)
	} else if !strings.Contains(sdl, "Film") {
		t.Errorf("expected 'Film' in SDL, got %q", sdl)
	}
}

// TestLiveGraphCDNFallback proves the shallow-query fallback works
// against an upstream that rejects the standard depth-9
// introspection. trevorblades + rickandmortyapi both sit behind
// Graph CDN / Stellate and return HTTP 413 GCDN_QUERY_DEPTH_LIMIT
// for the full query; the fallback path retries with a 5-level
// shallow query that passes.
func TestLiveGraphCDNFallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := NewClient("https://countries.trevorblades.com/", nil, &http.Client{Timeout: 20 * time.Second})
	s, mode, err := client.Introspect(ctx)
	if err != nil {
		t.Fatalf("introspect with fallback: %v", err)
	}
	if mode != IntrospectionShallow {
		t.Errorf("expected shallow mode for Graph CDN upstream, got %s", mode)
	}
	if len(s.Types) == 0 {
		t.Fatalf("expected types from shallow introspection")
	}
	t.Logf("Graph CDN fallback indexed %d types in %s mode", len(s.Types), mode)
}

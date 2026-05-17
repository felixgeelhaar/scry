//go:build live

package upstream

import (
	"context"
	"strings"
	"testing"
	"time"
)

// liveEndpoint mirrors the schema package's live target so the same
// upstream is exercised end-to-end (introspect → search → execute).
const liveEndpoint = "https://swapi-graphql.netlify.app/graphql"

func TestLiveSWAPIExecute(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c, err := New(Config{Endpoint: liveEndpoint, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	res, err := c.Execute(ctx, `{ allFilms { films { title director } } }`, nil, "")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != 200 {
		t.Errorf("status=%d, want 200", res.Status)
	}
	body := string(res.Raw)
	if !strings.Contains(body, "A New Hope") {
		t.Errorf("expected 'A New Hope' in response, got %q", body)
	}
	t.Logf("SWAPI response (%d bytes): %s", len(body), snippet(res.Raw))
}

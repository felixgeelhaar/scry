package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestRateLimitRejectsOverBurst confirms ErrRateLimited fires when
// the burst is exhausted, without hitting the upstream. Crucial that
// rejected requests don't bill against the upstream's quota — that
// would defeat the point of the gate.
func TestRateLimitRejectsOverBurst(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer srv.Close()

	cli, err := New(Config{
		Endpoint:  srv.URL,
		Auth:      AuthSpec{HasScheme: true, Scheme: "Bearer", Token: tokFunc("t")},
		Timeout:   2 * time.Second,
		RateLimit: RateLimitConfig{RPS: 1, Burst: 2},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// Two requests fit the burst.
	for i := 0; i < 2; i++ {
		if _, err := cli.Execute(context.Background(), `{ ok }`, nil, ""); err != nil {
			t.Fatalf("burst call %d: %v", i, err)
		}
	}
	// Third should be rate-limited.
	_, err = cli.Execute(context.Background(), `{ ok }`, nil, "")
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("expected ErrRateLimited on burst exceed, got %v", err)
	}
	if got := atomic.LoadInt64(&hits); got != 2 {
		t.Errorf("rate-limited request leaked to upstream: hits=%d, want 2", got)
	}
}

// TestRateLimitDisabledWhenZero confirms the zero-value config skips
// the gate entirely — operators who never set rate_limit should see
// no behavioural change.
func TestRateLimitDisabledWhenZero(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer srv.Close()

	cli, err := New(Config{
		Endpoint: srv.URL,
		Auth:     AuthSpec{HasScheme: true, Scheme: "Bearer", Token: tokFunc("t")},
		Timeout:  2 * time.Second,
		// RateLimit zero
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	// Fire 5 in quick succession — should all reach upstream.
	for i := 0; i < 5; i++ {
		if _, err := cli.Execute(context.Background(), `{ ok }`, nil, ""); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt64(&hits); got != 5 {
		t.Errorf("hits=%d, want 5 (rate limit unintentionally engaged)", got)
	}
}

// TestRateLimitDefaultBurst confirms Burst defaults to RPS when
// omitted, so `rps: 5` alone gives 5 burstable requests not 1.
func TestRateLimitDefaultBurst(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer srv.Close()

	cli, err := New(Config{
		Endpoint:  srv.URL,
		Auth:      AuthSpec{HasScheme: true, Scheme: "Bearer", Token: tokFunc("t")},
		Timeout:   2 * time.Second,
		RateLimit: RateLimitConfig{RPS: 3}, // no burst
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := cli.Execute(context.Background(), `{ ok }`, nil, ""); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt64(&hits); got != 3 {
		t.Errorf("hits=%d, want 3 (default burst should match RPS)", got)
	}
}

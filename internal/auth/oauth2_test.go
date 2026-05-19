package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func fakeTokenServer(t *testing.T, calls *atomic.Int64, tokenTTL time.Duration) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok-" + r.URL.Path,
			"token_type":   "Bearer",
			"expires_in":   int(tokenTTL.Seconds()),
		})
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestNewOAuth2TokenSourceValidates(t *testing.T) {
	if _, err := NewOAuth2TokenSource(context.Background(), &OAuth2Config{}); err == nil {
		t.Errorf("empty config must fail validation")
	}
	if _, err := NewOAuth2TokenSource(context.Background(), &OAuth2Config{
		TokenURL: "https://x.example.com/token",
	}); err == nil {
		t.Errorf("missing client_id must fail validation")
	}
}

func TestOAuth2TokenSourceIssuesToken(t *testing.T) {
	var calls atomic.Int64
	srv := fakeTokenServer(t, &calls, time.Hour)
	src, err := NewOAuth2TokenSource(context.Background(), &OAuth2Config{
		TokenURL:     srv.URL,
		ClientID:     "abc",
		ClientSecret: "secret",
	})
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	tok, err := src.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok.AccessToken == "" {
		t.Errorf("expected access token, got empty")
	}
	if calls.Load() != 1 {
		t.Errorf("expected 1 token-endpoint call, got %d", calls.Load())
	}
}

func TestOAuth2TokenSourceCachesValidToken(t *testing.T) {
	var calls atomic.Int64
	srv := fakeTokenServer(t, &calls, time.Hour)
	src, err := NewOAuth2TokenSource(context.Background(), &OAuth2Config{
		TokenURL:     srv.URL,
		ClientID:     "abc",
		ClientSecret: "secret",
		RefreshSkew:  time.Second,
	})
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := src.Token(); err != nil {
			t.Fatalf("token[%d]: %v", i, err)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("repeated calls within validity should hit cache; got %d token-endpoint calls", calls.Load())
	}
}

func TestOAuth2TokenSourceRefreshesAheadOfExpiry(t *testing.T) {
	var calls atomic.Int64
	// Very short TTL → skew immediately marks the token as
	// expiring-soon so the next Token() triggers a refresh.
	srv := fakeTokenServer(t, &calls, time.Second)
	src, err := NewOAuth2TokenSource(context.Background(), &OAuth2Config{
		TokenURL:     srv.URL,
		ClientID:     "abc",
		ClientSecret: "secret",
		RefreshSkew:  2 * time.Second, // skew > TTL → always expiresSoon
	})
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := src.Token(); err != nil {
			t.Fatalf("token[%d]: %v", i, err)
		}
	}
	if calls.Load() != 3 {
		t.Errorf("skew > TTL should force per-call refresh; got %d", calls.Load())
	}
}

func TestExpiresSoonHandlesZeroExpiry(t *testing.T) {
	// Tokens without a reported Expiry must not be treated as
	// expiring — matches stock oauth2 semantics.
	s := &skewedSource{skew: time.Hour}
	if s.expiresSoon(nil) {
		t.Errorf("nil token: expiresSoon must be false")
	}
}

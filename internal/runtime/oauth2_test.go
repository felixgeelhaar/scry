package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felixgeelhaar/scry/internal/auth"
)

// TestOAuth2AddSendsBearerOnUpstream wires an OAuth2 client-credentials
// AddConfig + a fake token endpoint + a fake GraphQL upstream that
// asserts the Authorization header carries the access token from
// the token endpoint. End-to-end smoke of the OAuth2 runtime path.
func TestOAuth2AddSendsBearerOnUpstream(t *testing.T) {
	var tokenCalls atomic.Int64
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tokenCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "minted-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	}))
	t.Cleanup(tokenServer.Close)

	var sawAuthHeader atomic.Value // string
	graphQLServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuthHeader.Store(r.Header.Get("Authorization"))
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "__schema") {
			_, _ = w.Write([]byte(`{"data":{"__schema":{"queryType":{"name":"Query"},"types":[{"kind":"OBJECT","name":"Query","fields":[{"name":"ping","type":{"kind":"SCALAR","name":"String"}}]}]}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"ping":"pong"}}`))
	}))
	t.Cleanup(graphQLServer.Close)

	indexDir := t.TempDir()
	mgr, err := New(indexDir, 1000)
	if err != nil {
		t.Fatalf("mgr: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := mgr.Add(ctx, AddConfig{
		Name:     "default",
		Upstream: graphQLServer.URL,
		OAuth2: &auth.OAuth2Config{
			TokenURL:     tokenServer.URL,
			ClientID:     "client-x",
			ClientSecret: "secret-x",
		},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if tokenCalls.Load() == 0 {
		t.Errorf("token endpoint should have been hit during introspection; got 0 calls")
	}
	got, _ := sawAuthHeader.Load().(string)
	if got != "Bearer minted-access-token" {
		t.Errorf("upstream Authorization header = %q, want Bearer minted-access-token", got)
	}
}

func TestAddConfigFromServerWiresOAuth2(t *testing.T) {
	srv := auth.Server{
		Upstream: "https://x.example.com/graphql",
		Auth: auth.Auth{
			Type: "oauth2",
			OAuth2: &auth.OAuth2Config{
				TokenURL:     "https://x.example.com/token",
				ClientID:     "id",
				ClientSecret: "sec",
			},
		},
	}
	cfg := addConfigFromServer("x", srv)
	if cfg.OAuth2 == nil {
		t.Fatalf("OAuth2 not propagated")
	}
	if cfg.AuthRef != "" {
		t.Errorf("AuthRef should be empty so buildAuthSpec takes the OAuth2 branch; got %q", cfg.AuthRef)
	}
}

func TestAddConfigFromServerSkipsOAuth2WhenTypeIsBearer(t *testing.T) {
	srv := auth.Server{
		Upstream: "https://x.example.com/graphql",
		Auth: auth.Auth{
			Type:  "bearer",
			Token: "env://X_TOK",
		},
	}
	cfg := addConfigFromServer("x", srv)
	if cfg.OAuth2 != nil {
		t.Errorf("bearer type must not populate OAuth2 even if struct present")
	}
	if cfg.AuthRef != "env://X_TOK" {
		t.Errorf("bearer ref should map to AuthRef")
	}
}

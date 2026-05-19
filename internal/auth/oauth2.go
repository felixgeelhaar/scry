package auth

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// OAuth2Config holds the per-upstream OAuth2 client-credentials
// configuration scry needs to issue a TokenSource. Mirrors the
// servers.yml shape:
//
//	auth:
//	  type: oauth2
//	  token_url: https://upstream/oauth/token
//	  client_id: env://X_CLIENT_ID
//	  client_secret: env://X_CLIENT_SECRET
//	  scopes: [read:graphql]
//	  refresh_skew: 60s   # optional; refresh this many seconds
//	                      # before exp (default 60s)
//
// client_id + client_secret accept the same token-ref schemes as
// regular bearer tokens (env://, file://, op://, literal). They're
// resolved via ResolveToken at TokenSource-build time.
type OAuth2Config struct {
	TokenURL     string        `yaml:"token_url"`
	ClientID     string        `yaml:"client_id"`
	ClientSecret string        `yaml:"client_secret"`
	Scopes       []string      `yaml:"scopes,omitempty"`
	RefreshSkew  time.Duration `yaml:"refresh_skew,omitempty"`
}

// Validate runs the cheap checks before scry attempts a real
// token-endpoint exchange.
func (c *OAuth2Config) Validate() error {
	if c == nil {
		return fmt.Errorf("oauth2 config is nil")
	}
	if strings.TrimSpace(c.TokenURL) == "" {
		return fmt.Errorf("oauth2: token_url is required")
	}
	if strings.TrimSpace(c.ClientID) == "" {
		return fmt.Errorf("oauth2: client_id is required")
	}
	if strings.TrimSpace(c.ClientSecret) == "" {
		return fmt.Errorf("oauth2: client_secret is required")
	}
	return nil
}

// NewOAuth2TokenSource resolves the client_id + client_secret refs
// and returns a TokenSource that automatically refreshes ahead of
// expiry. Honours RefreshSkew (default 60s).
//
// The returned source is safe for concurrent use — clientcredentials'
// reuseTokenSource already serialises refreshes.
func NewOAuth2TokenSource(ctx context.Context, c *OAuth2Config) (oauth2.TokenSource, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	clientID, err := ResolveToken(c.ClientID)
	if err != nil {
		return nil, fmt.Errorf("oauth2: resolve client_id: %w", err)
	}
	clientSecret, err := ResolveToken(c.ClientSecret)
	if err != nil {
		return nil, fmt.Errorf("oauth2: resolve client_secret: %w", err)
	}
	cfg := clientcredentials.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     c.TokenURL,
		Scopes:       c.Scopes,
	}
	skew := c.RefreshSkew
	if skew <= 0 {
		skew = 60 * time.Second
	}
	base := cfg.TokenSource(ctx)
	return &skewedSource{base: base, skew: skew}, nil
}

// skewedSource is a TokenSource wrapper that proactively triggers a
// refresh `skew` ahead of the upstream's reported expiry. Stock
// clientcredentials only refreshes AFTER expiry — by the time the
// next request fires, the in-flight call sees a 401. The skew avoids
// the auth_expired storm by treating tokens as expired earlier than
// the issuer says.
type skewedSource struct {
	base oauth2.TokenSource
	skew time.Duration

	mu      sync.Mutex
	current *oauth2.Token
}

func (s *skewedSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil && !s.expiresSoon(s.current) {
		return s.current, nil
	}
	t, err := s.base.Token()
	if err != nil {
		return nil, err
	}
	s.current = t
	return t, nil
}

func (s *skewedSource) expiresSoon(t *oauth2.Token) bool {
	if t == nil || t.Expiry.IsZero() {
		// Server didn't report expiry — treat as never-expires
		// (matches stock oauth2 behaviour).
		return false
	}
	return time.Now().Add(s.skew).After(t.Expiry)
}

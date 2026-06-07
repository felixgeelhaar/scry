// Package upstream wraps the fortify-protected HTTP client that talks
// to the upstream GraphQL endpoint. Single responsibility: take a
// GraphQL query, POST it to the configured endpoint, return the raw
// JSON body. Everything resilience-related (retry, circuit breaker,
// timeout) lives in the fortify chain configured at construction.
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"go.klarlabs.de/fortify/middleware"
	"go.klarlabs.de/fortify/ratelimit"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	otelmetric "go.opentelemetry.io/otel/metric"

	"github.com/felixgeelhaar/scry/internal/obs"
)

// Client is the resilience-wrapped GraphQL client. One per upstream
// — the fortify chain is shared across all query_execute calls so
// circuit-breaker state accumulates across requests.
//
// The auth spec is held in an atomic.Pointer so SetAuth can swap it
// on the fly when servers.yml hot-reloads a rotated token, header
// name, or scheme. Lock-free on the hot path (one atomic Load per
// Execute call).
type Client struct {
	endpoint string
	http     *http.Client
	auth     atomic.Pointer[AuthSpec]
}

// AuthSpec is the full credential shape one upstream needs: a header
// name, an optional scheme prefix, and a function that resolves the
// current secret value. Bundling them lets SetAuth swap header /
// scheme / token together — important because the three change
// together (per server) in servers.yml hot-reloads.
//
// HasScheme=false means "no prefix" — emit the header value as the
// raw token. HasScheme=true uses Scheme + " " + Token. Empty Header
// means "Authorization".
type AuthSpec struct {
	Header    string
	Scheme    string
	HasScheme bool
	Token     func() string
}

// Config configures the upstream client. All fields have sensible
// defaults; the only required field is Endpoint.
type Config struct {
	Endpoint string
	// Auth is the credential spec. When Auth.Token is non-nil it is
	// invoked once per request to fetch the current value, so
	// servers.yml hot-reload of a rotated token works without
	// rebuilding the client. Nil Token disables the auth header
	// entirely.
	Auth AuthSpec
	// Timeout caps individual upstream calls. Default 30s.
	Timeout time.Duration
	// MaxRetries bounds attempts. Default 3.
	MaxRetries int
	// RateLimit caps RPS to this upstream via a token bucket. Zero
	// disables. Burst defaults to RPS rounded up when omitted.
	RateLimit RateLimitConfig
}

// RateLimitConfig configures the per-client token-bucket gate.
// Mirrors auth.RateLimit but lives here so the upstream package
// doesn't need to depend on auth.
type RateLimitConfig struct {
	RPS   float64
	Burst int
}

// New constructs a Client. Returns an error if the fortify chain
// cannot be built (e.g. invalid timeout).
func New(cfg Config) (*Client, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("upstream: endpoint is required")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	// Fortify's HTTPClient returns a typed Chain[*http.Response], not
	// an open response — bodyclose's pattern-match for *http.Response
	// here is a false positive.
	//nolint:bodyclose // resilience-chain constructor, not an HTTP response
	chain, err := middleware.HTTPClient(middleware.HTTPClientConfig{
		Timeout:    cfg.Timeout,
		MaxRetries: cfg.MaxRetries,
	})
	if err != nil {
		return nil, fmt.Errorf("upstream: build fortify chain: %w", err)
	}
	rt := middleware.HTTPRoundTripperFromChain(http.DefaultTransport, chain)
	if cfg.RateLimit.RPS > 0 {
		burst := cfg.RateLimit.Burst
		if burst <= 0 {
			burst = int(cfg.RateLimit.RPS)
			if burst < 1 {
				burst = 1
			}
		}
		lim := ratelimit.New(ratelimit.Config{
			Rate:     int(cfg.RateLimit.RPS),
			Burst:    burst,
			Interval: time.Second,
		})
		rt = &rateLimitedTransport{inner: rt, lim: lim, endpoint: cfg.Endpoint}
	}
	c := &Client{
		endpoint: cfg.Endpoint,
		http:     &http.Client{Transport: rt, Timeout: cfg.Timeout},
	}
	if cfg.Auth.Token != nil {
		c.auth.Store(&cfg.Auth)
	}
	return c, nil
}

// SetAuth swaps the credential spec atomically. Used by the
// hot-reload path so a rotated token / header / scheme in
// servers.yml takes effect without rebuilding the client
// (preserves circuit-breaker state + the fortify chain).
func (c *Client) SetAuth(spec AuthSpec) {
	c.auth.Store(&spec)
}

// Result captures one Execute round-trip. Raw is the upstream's body
// passed through unmodified — agents get whatever the upstream
// returned, including the standard `{ data, errors, extensions }`
// envelope. Status is the HTTP status code.
type Result struct {
	Status int
	Raw    json.RawMessage
}

// ErrAuthExpired is returned when the upstream responds with 401.
// Carries through to the MCP tool so the agent can call auth_login
// per the auth-design.md recovery loop.
var ErrAuthExpired = errors.New("upstream: auth expired")

// ErrRateLimited is returned when scry's per-server rate limiter
// rejects a request before it leaves the process. Distinct from
// upstream 429s — the upstream never saw this call. Agents that
// see it should back off (the envelope hint includes a retry-after
// suggestion) instead of retrying immediately.
var ErrRateLimited = errors.New("upstream: rate limited")

// rateLimitedTransport gates outbound requests through a token
// bucket. When Allow returns false, fail closed with ErrRateLimited
// — no upstream call, no timer eaten — so the caller can surface
// the back-pressure to the agent before retrying.
type rateLimitedTransport struct {
	inner    http.RoundTripper
	lim      ratelimit.RateLimiter
	endpoint string
}

func (t *rateLimitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Key by endpoint so multiple Clients sharing this transport
	// path (today: one per upstream) stay isolated. Single-endpoint
	// Clients still benefit because the key is stable across calls.
	if !t.lim.Allow(req.Context(), t.endpoint) {
		return nil, ErrRateLimited
	}
	return t.inner.RoundTrip(req)
}

// Execute POSTs `query` (plus optional variables and operationName)
// to the configured endpoint. Returns the upstream's raw response.
// Transport-level retries are handled inside the fortify chain;
// callers see the final outcome only.
//
// Returns ErrAuthExpired specifically on 401 so the MCP layer can map it
// to an auth_expired envelope; all other non-2xx responses surface
// as ordinary errors with the upstream's body preview.
func (c *Client) Execute(ctx context.Context, query string, variables map[string]any, opName string) (*Result, error) {
	ctx, span := otel.Tracer("github.com/felixgeelhaar/scry").Start(ctx, "upstream.execute")
	defer span.End()
	span.SetAttributes(
		attribute.String("upstream.endpoint", c.endpoint),
		attribute.Int("graphql.query_len", len(query)),
		attribute.String("graphql.operation_name", opName),
	)
	start := time.Now()
	defer func() {
		obs.Metrics().UpstreamLatency.Record(ctx, time.Since(start).Seconds(), otelmetric.WithAttributes(
			attribute.String("op", "execute"),
		))
	}()

	payload := map[string]any{"query": query}
	if len(variables) > 0 {
		payload["variables"] = variables
	}
	if opName != "" {
		payload["operationName"] = opName
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint, bytes.NewReader(body))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "build request")
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if spec := c.auth.Load(); spec != nil && spec.Token != nil {
		if tok := spec.Token(); tok != "" {
			header := spec.Header
			if header == "" {
				header = "Authorization"
			}
			value := tok
			if spec.HasScheme && spec.Scheme != "" {
				value = spec.Scheme + " " + tok
			}
			req.Header.Set(header, value)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "transport")
		return nil, fmt.Errorf("upstream: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4 MiB ceiling
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	span.SetAttributes(
		attribute.Int("http.status_code", resp.StatusCode),
		attribute.Int("graphql.response_bytes", len(raw)),
	)
	if resp.StatusCode == http.StatusUnauthorized {
		span.SetStatus(codes.Error, "auth_expired")
		return &Result{Status: resp.StatusCode, Raw: raw}, ErrAuthExpired
	}
	if resp.StatusCode/100 != 2 {
		span.SetStatus(codes.Error, fmt.Sprintf("upstream %d", resp.StatusCode))
		return &Result{Status: resp.StatusCode, Raw: raw},
			fmt.Errorf("upstream returned %d: %s", resp.StatusCode, snippet(raw))
	}
	return &Result{Status: resp.StatusCode, Raw: raw}, nil
}

func snippet(b []byte) string {
	const max = 200
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "..."
}

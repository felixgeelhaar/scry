package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExecuteSendsQueryAndReturnsBody(t *testing.T) {
	var seenBody string
	var seenAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		seenBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"ping":"pong"}}`))
	}))
	defer srv.Close()

	c, err := New(Config{
		Endpoint: srv.URL,
		Auth:     AuthSpec{HasScheme: true, Scheme: "Bearer", Token: func() string { return "test-token" }},
		Timeout:  2 * time.Second,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	res, err := c.Execute(context.Background(), `{ ping }`, map[string]any{"x": 1}, "Ping")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != 200 {
		t.Errorf("status=%d, want 200", res.Status)
	}
	if !strings.Contains(seenBody, `"query":"{ ping }"`) {
		t.Errorf("body missing query: %s", seenBody)
	}
	if !strings.Contains(seenBody, `"operationName":"Ping"`) {
		t.Errorf("body missing operationName: %s", seenBody)
	}
	if !strings.Contains(seenBody, `"variables":{"x":1}`) {
		t.Errorf("body missing variables: %s", seenBody)
	}
	if seenAuth != "Bearer test-token" {
		t.Errorf("auth header = %q, want 'Bearer test-token'", seenAuth)
	}
	if !strings.Contains(string(res.Raw), `"pong"`) {
		t.Errorf("response missing pong: %s", res.Raw)
	}
}

func TestExecuteMapsUnauthorizedToErrAuthExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"errors":[{"message":"token expired"}]}`))
	}))
	defer srv.Close()

	c, _ := New(Config{Endpoint: srv.URL, Timeout: 2 * time.Second})
	_, err := c.Execute(context.Background(), `{ ping }`, nil, "")
	if !errors.Is(err, ErrAuthExpired) {
		t.Errorf("expected ErrAuthExpired, got %v", err)
	}
}

func TestExecuteSurfacesUpstreamFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`internal error`))
	}))
	defer srv.Close()

	c, _ := New(Config{Endpoint: srv.URL, Timeout: 2 * time.Second, MaxRetries: 1})
	res, err := c.Execute(context.Background(), `{ ping }`, nil, "")
	if err == nil {
		t.Fatalf("expected error for 500")
	}
	if errors.Is(err, ErrAuthExpired) {
		t.Errorf("500 should not be ErrAuthExpired")
	}
	if res == nil || res.Status != 500 {
		t.Errorf("expected status=500 in result, got %+v", res)
	}
}

func TestNewRejectsEmptyEndpoint(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatalf("expected error for empty endpoint")
	}
}

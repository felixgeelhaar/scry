package upstream

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestExecuteHonoursCustomHeaderAndScheme exercises the four
// permutations of the new auth header surface:
//
//   - default (Authorization: Bearer <T>)
//   - custom header (X-API-Key: Bearer <T>)
//   - custom scheme on default header (Authorization: Token <T>)
//   - no-scheme + custom header (X-API-Key: <T> raw)
//
// Each case spins up a fake upstream that captures the inbound
// headers and asserts the wire shape matches what scry should have
// emitted.
func TestExecuteHonoursCustomHeaderAndScheme(t *testing.T) {
	empty := ""
	customScheme := "Token"
	cases := []struct {
		name        string
		spec        AuthSpec
		wantHeader  string
		wantValue   string
		wantSkipped bool
	}{
		{
			name:       "default header + default scheme",
			spec:       AuthSpec{HasScheme: true, Scheme: "Bearer", Token: tokFunc("t1")},
			wantHeader: "Authorization",
			wantValue:  "Bearer t1",
		},
		{
			name:       "custom header keeps default Bearer scheme",
			spec:       AuthSpec{Header: "X-API-Key", HasScheme: true, Scheme: "Bearer", Token: tokFunc("t2")},
			wantHeader: "X-API-Key",
			wantValue:  "Bearer t2",
		},
		{
			name:       "default header + custom Token scheme",
			spec:       AuthSpec{HasScheme: true, Scheme: customScheme, Token: tokFunc("t3")},
			wantHeader: "Authorization",
			wantValue:  "Token t3",
		},
		{
			name:       "custom header + raw value (no scheme)",
			spec:       AuthSpec{Header: "X-API-Key", HasScheme: false, Scheme: empty, Token: tokFunc("t4")},
			wantHeader: "X-API-Key",
			wantValue:  "t4",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var seen http.Header
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = r.Header.Clone()
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
			}))
			defer srv.Close()

			cli, err := New(Config{Endpoint: srv.URL, Auth: c.spec, Timeout: 2 * time.Second})
			if err != nil {
				t.Fatalf("new: %v", err)
			}
			if _, err := cli.Execute(context.Background(), `{ ok }`, nil, ""); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if got := seen.Get(c.wantHeader); got != c.wantValue {
				t.Errorf("%s = %q, want %q (full headers: %v)", c.wantHeader, got, c.wantValue, seen)
			}
		})
	}
}

// TestSetAuthSwapsHeaderAndSchemeOnTheFly proves the hot-reload
// promise: after SetAuth, subsequent calls use the new header +
// scheme without rebuilding the client.
func TestSetAuthSwapsHeaderAndSchemeOnTheFly(t *testing.T) {
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer srv.Close()

	cli, err := New(Config{
		Endpoint: srv.URL,
		Auth:     AuthSpec{HasScheme: true, Scheme: "Bearer", Token: tokFunc("v1")},
		Timeout:  2 * time.Second,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	_, _ = cli.Execute(context.Background(), `{ ok }`, nil, "")
	if got := seen.Get("Authorization"); got != "Bearer v1" {
		t.Fatalf("initial Authorization = %q, want 'Bearer v1'", got)
	}

	cli.SetAuth(AuthSpec{Header: "X-API-Key", HasScheme: false, Token: tokFunc("v2")})
	_, _ = cli.Execute(context.Background(), `{ ok }`, nil, "")
	if got := seen.Get("Authorization"); got != "" {
		t.Errorf("after rotation, Authorization should be empty, got %q", got)
	}
	if got := seen.Get("X-API-Key"); got != "v2" {
		t.Errorf("after rotation, X-API-Key = %q, want 'v2'", got)
	}
}

func tokFunc(v string) func() string { return func() string { return v } }

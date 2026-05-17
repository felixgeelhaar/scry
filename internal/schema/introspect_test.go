package schema

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsDepthLimitErrorMatchesGraphCDN(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"413 graph cdn", &httpError{status: 413, body: `{"errors":[{"message":"Query depth limit exceeded."}]}`}, true},
		{"400 with depth body", &httpError{status: 400, body: "max query depth exceeded"}, true},
		{"400 with complexity body", &httpError{status: 400, body: "Query complexity is too high"}, true},
		{"401 auth not depth", &httpError{status: 401, body: "Unauthorized"}, false},
		{"500 server error", &httpError{status: 500, body: "internal"}, false},
		{"graphql error mentions depth", &graphqlError{message: "Query exceeds maximum depth of 7"}, true},
		{"graphql error mentions complexity", &graphqlError{message: "complexity score 1000 over limit"}, true},
		{"graphql error unrelated", &graphqlError{message: "Field not found"}, false},
		{"plain wrapped error", errors.New("network unreachable"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isDepthLimitError(c.err); got != c.want {
				t.Errorf("isDepthLimitError(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// TestIntrospectFallsBackOnDepthLimit drives the full Introspect()
// flow against a synthetic upstream that mimics Graph CDN: rejects
// the deep query with 413 + GCDN_QUERY_DEPTH_LIMIT, accepts the
// shallow query and returns a minimal valid introspection payload.
func TestIntrospectFallsBackOnDepthLimit(t *testing.T) {
	var deepRejects, shallowAccepts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read the body to tell deep vs shallow apart. Deep
		// query has 4 ofType levels; shallow has 1.
		buf := make([]byte, 8192)
		n, _ := r.Body.Read(buf)
		body := string(buf[:n])
		// Crude but sufficient: deep query nests ofType four
		// times. Shallow has it once.
		if countSubstr(body, "ofType") >= 7 {
			deepRejects++
			w.WriteHeader(413)
			_, _ = w.Write([]byte(`{"errors":[{"message":"Query depth limit exceeded.","extensions":{"code":"GCDN_QUERY_DEPTH_LIMIT"}}]}`))
			return
		}
		shallowAccepts++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"__schema":{"queryType":{"name":"Query"},"types":[{"kind":"OBJECT","name":"Query","fields":[{"name":"hello","type":{"kind":"SCALAR","name":"String"}}]}]}}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil, srv.Client())
	s, mode, err := c.Introspect(context.Background())
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if mode != IntrospectionShallow {
		t.Errorf("expected mode=shallow, got %s", mode)
	}
	if deepRejects != 1 || shallowAccepts != 1 {
		t.Errorf("expected 1 deep reject + 1 shallow accept, got deep=%d shallow=%d", deepRejects, shallowAccepts)
	}
	if len(s.Types) == 0 {
		t.Errorf("expected types from shallow query")
	}
}

func TestIntrospectPropagatesNonDepthErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"errors":[{"message":"Unauthorized"}]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil, srv.Client())
	_, _, err := c.Introspect(context.Background())
	if err == nil {
		t.Fatalf("expected error for 401, got nil")
	}
}

func countSubstr(s, sub string) int {
	n := 0
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			n++
		}
	}
	return n
}

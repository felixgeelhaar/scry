package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// startFakeUpstream stands up a httptest server that answers any
// POST with a minimal-but-valid introspection payload. Used by the
// transport smoke tests so Run()'s initial introspection step
// doesn't have to talk to the public internet.
func startFakeUpstream(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "data": {
    "__schema": {
      "queryType": {"name": "Query"},
      "types": [
        {"kind": "OBJECT", "name": "Query", "fields": [{"name": "ping", "type": {"kind": "SCALAR", "name": "String"}}]}
      ]
    }
  }
}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

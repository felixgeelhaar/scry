//go:build stdio_smoke

// MCP stdio wire-protocol smoke. Spawns the real scry binary as a
// subprocess, drives initialize → tools/list → tools/call over
// line-delimited JSON-RPC, asserts the responses look right.
//
// Build-tagged so the default `go test ./...` doesn't pay the
// `go run` compile cost. Run with:
//
//	go test -tags=stdio_smoke -run TestStdio ./internal/server/...
package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestStdioSmoke exercises the full client→server wire protocol over
// stdio. Points the binary at a httptest GraphQL upstream so the
// test is hermetic — no network, no public endpoint to break it.
func TestStdioSmoke(t *testing.T) {
	upstream := startStdioFakeUpstream(t)
	bin := buildScryBinary(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	indexDir := t.TempDir()
	cmd := exec.CommandContext(ctx, bin, "serve",
		"--upstream", upstream,
		"--refresh-interval", "0",
		"--transport", "stdio",
		"--index", indexDir,
	)
	cmd.Env = []string{"SCRY_LOG_LEVEL=error", "PATH=/usr/bin:/bin"} // hermetic, no inherited tokens
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start scry: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	})
	// Surface stderr in test logs so failures point at the
	// server side, not just the client side.
	go func() {
		b, _ := io.ReadAll(stderr)
		if len(b) > 0 {
			t.Logf("scry stderr: %s", b)
		}
	}()

	reader := bufio.NewReader(stdout)

	// 1. initialize handshake.
	send(t, stdin, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "scry-smoke",
				"version": "0.0.0",
			},
		},
	})
	resp := recv(t, reader)
	if resp["error"] != nil {
		t.Fatalf("initialize errored: %+v", resp["error"])
	}
	result, _ := resp["result"].(map[string]any)
	if result == nil {
		t.Fatalf("initialize: missing result: %+v", resp)
	}
	info, _ := result["serverInfo"].(map[string]any)
	if info == nil || info["name"] != "scry" {
		t.Errorf("initialize: serverInfo.name = %v, want scry", info)
	}

	// 2. notifications/initialized — required by MCP spec before
	//    any other request.
	send(t, stdin, map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})

	// 3. tools/list — must return our 7 tools.
	send(t, stdin, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	})
	resp = recv(t, reader)
	if resp["error"] != nil {
		t.Fatalf("tools/list errored: %+v", resp["error"])
	}
	result, _ = resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	gotNames := map[string]bool{}
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		if n, ok := tool["name"].(string); ok {
			gotNames[n] = true
		}
	}
	expected := []string{
		"schema_search", "schema_get", "schema_diff",
		"query_validate", "query_cost", "query_execute",
		"auth_status", "auth_login",
		"list_servers",
		"gate_status", "gate_chain",
	}
	for _, name := range expected {
		if !gotNames[name] {
			t.Errorf("tools/list missing %q (got %v)", name, gotNames)
		}
	}

	// 4. tools/call schema_search — should return the markdown
	//    table our handler builds from FTS5 hits.
	send(t, stdin, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "schema_search",
			"arguments": map[string]any{"query": "ping", "limit": 5},
		},
	})
	resp = recv(t, reader)
	if resp["error"] != nil {
		t.Fatalf("tools/call schema_search errored: %+v", resp["error"])
	}
	result, _ = resp["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("schema_search returned empty content: %+v", result)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "ping") && !strings.Contains(text, "No schema units match") {
		t.Errorf("schema_search response unexpected: %q", text)
	}
}

// send writes one JSON-RPC frame to stdin. MCP stdio is
// line-delimited: one JSON object per line, terminated by \n.
func send(t *testing.T, w io.Writer, msg map[string]any) {
	t.Helper()
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

// recv reads one JSON-RPC frame. Returns the parsed envelope.
// Skips any non-JSON output (defensive — bolt logs would normally
// go to stderr but a misconfigured run could send them here).
func recv(t *testing.T, r *bufio.Reader) map[string]any {
	t.Helper()
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read frame: %v (partial %q)", err, line)
		}
		line = []byte(strings.TrimSpace(string(line)))
		if len(line) == 0 {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal(line, &msg); err != nil {
			t.Logf("skipping non-JSON line: %q", line)
			continue
		}
		return msg
	}
}

// startStdioFakeUpstream stands up a tiny GraphQL endpoint with
// exactly the introspection response scry needs to boot. Defined
// locally so this file is self-contained — the transport test's
// fake_upstream_test.go isn't visible under the stdio_smoke build
// tag.
func startStdioFakeUpstream(t *testing.T) string {
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

// buildScryBinary compiles cmd/scry into a tempdir binary so the
// test exercises the real binary path users install. Builds once
// per test invocation; subsequent tests reuse the result.
func buildScryBinary(t *testing.T) string {
	t.Helper()
	binPath := fmt.Sprintf("%s/scry", t.TempDir())
	cmd := exec.Command("go", "build", "-o", binPath, "github.com/felixgeelhaar/scry/cmd/scry")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build scry: %v\n%s", err, out)
	}
	return binPath
}

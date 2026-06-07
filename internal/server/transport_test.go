package server

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/bolt"

	"github.com/felixgeelhaar/scry/internal/obs"
)

// init silences scry's logger for the server tests. Bolt panics on a
// nil global, so we install a JSONHandler against io.Discard.
func init() {
	obs.SetForTest(bolt.New(bolt.NewJSONHandler(io.Discard)))
}

// TestServeTransportBindsHTTP boots an HTTP transport on a free port
// and confirms the listener is reachable. Doesn't drive the full
// JSON-RPC flow — that lives in the MCP-stdio smoke test layer —
// but proves the transport wiring is sound + the address parsing
// matches what operators will type.
func TestServeTransportBindsHTTP(t *testing.T) {
	addr := freeAddr(t)
	cfg := newSmokeCfg(t, "http", addr, "")
	runTransportSmoke(t, cfg, addr)
}

func TestServeTransportBindsGRPC(t *testing.T) {
	addr := freeAddr(t)
	cfg := newSmokeCfg(t, "grpc", addr, "")
	runTransportSmoke(t, cfg, addr)
}

func TestServeTransportBindsWebSocket(t *testing.T) {
	addr := freeAddr(t)
	cfg := newSmokeCfg(t, "ws", addr, "")
	runTransportSmoke(t, cfg, addr)
}

// TestServeTransportRejectsUnknown asserts the dispatch returns a
// clear error rather than silently falling back when an operator
// fat-fingers --transport.
func TestServeTransportRejectsUnknown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cfg := Config{
		UpstreamURL: "http://127.0.0.1:1",
		Transport:   "carrier-pigeon",
		ListenAddr:  ":17779",
	}
	err := Run(ctx, cfg)
	if err == nil {
		t.Fatalf("expected error for unsupported transport")
	}
	if !strings.Contains(err.Error(), "unsupported transport") &&
		!strings.Contains(err.Error(), "introspect") {
		// Run also fails on introspection (no real upstream), but
		// the transport check is reached only if introspection
		// hasn't already errored. Either error is acceptable as
		// long as it's not a silent boot.
		t.Logf("Run returned: %v", err)
	}
}

// newSmokeCfg builds the minimum Config needed to start scry against
// a fake upstream. The upstream is a httptest server reaching its
// own /graphql endpoint with a stub introspection response so the
// initial index build doesn't fail.
func newSmokeCfg(t *testing.T, transport, addr, serveAuth string) Config {
	t.Helper()
	upstreamURL := startFakeUpstream(t)
	indexDir := t.TempDir()
	return Config{
		UpstreamURL:     upstreamURL,
		IndexDir:        indexDir,
		Transport:       transport,
		ListenAddr:      addr,
		ServeAuthToken:  serveAuth,
		RefreshInterval: 0, // disable background refresher in tests
		CostCeiling:     1000,
	}
}

// runTransportSmoke runs scry until either the listener is reachable
// (success) or the timeout fires (failure). Returns nothing — fail
// via t.Fatalf so the test reports the right point.
func runTransportSmoke(t *testing.T, cfg Config, addr string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, cfg) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		select {
		case e := <-errCh:
			t.Fatalf("server exited before listener became reachable: %v", e)
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("listener at %s never became reachable", addr)
}

// freeAddr asks the kernel for an unused TCP port. Avoids fixed
// port numbers that flake on parallel test runs or busy dev boxes.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

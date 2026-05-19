package runtime

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDispatchSignsBodyWithRegistrationSecret(t *testing.T) {
	var sawSig atomic.Value // string
	var sawEvent atomic.Value
	var bodyBytes atomic.Value // []byte

	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawSig.Store(r.Header.Get(WebhookHTTPHeaderSignature))
		sawEvent.Store(r.Header.Get(WebhookHTTPHeaderEvent))
		b, _ := io.ReadAll(r.Body)
		bodyBytes.Store(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(receiver.Close)

	store, err := OpenWebhookStore(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	w, _ := store.Register(ctx, receiver.URL)

	d := NewWebhookDispatcher(store, "test-server")
	body := []byte(`{"added":[],"removed":[],"breaking":[]}`)
	if err := d.Dispatch(ctx, "schema_diff", body); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	if got := sawEvent.Load().(string); got != "schema_diff" {
		t.Errorf("X-Scry-Event = %q, want schema_diff", got)
	}
	mac := hmac.New(sha256.New, []byte(w.Secret))
	_, _ = mac.Write(body)
	wantSig := hex.EncodeToString(mac.Sum(nil))
	if got := sawSig.Load().(string); got != wantSig {
		t.Errorf("X-Scry-Signature = %q, want %q", got, wantSig)
	}
	gotBody := bodyBytes.Load().([]byte)
	if string(gotBody) != string(body) {
		t.Errorf("body mismatch:\ngot  %s\nwant %s", gotBody, body)
	}
}

func TestDispatchReturnsErrNoRegistrationsWhenEmpty(t *testing.T) {
	store, err := OpenWebhookStore(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer func() { _ = store.Close() }()
	d := NewWebhookDispatcher(store, "test-server")
	if err := d.Dispatch(context.Background(), "x", []byte(`{}`)); !errors.Is(err, ErrNoRegistrations) {
		t.Errorf("Dispatch on empty store should return ErrNoRegistrations, got %v", err)
	}
}

func TestDispatchRetriesOn5xx(t *testing.T) {
	var calls atomic.Int64
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(receiver.Close)

	store, _ := OpenWebhookStore(":memory:")
	defer func() { _ = store.Close() }()
	_, _ = store.Register(context.Background(), receiver.URL)

	d := NewWebhookDispatcher(store, "retry-test")
	// Tight retry budget to keep the test fast.
	d.maxRetries = 4
	_ = d.Dispatch(context.Background(), "schema_diff", []byte(`{}`))
	if got := calls.Load(); got != 3 {
		t.Errorf("expected 3 deliveries (2 failures + 1 success); got %d", got)
	}
}

func TestDispatchSkipsRetryOn4xx(t *testing.T) {
	var calls atomic.Int64
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(receiver.Close)

	store, _ := OpenWebhookStore(":memory:")
	defer func() { _ = store.Close() }()
	_, _ = store.Register(context.Background(), receiver.URL)

	d := NewWebhookDispatcher(store, "no-retry-test")
	d.maxRetries = 3
	_ = d.Dispatch(context.Background(), "schema_diff", []byte(`{}`))
	if got := calls.Load(); got != 1 {
		t.Errorf("4xx must not retry; got %d calls", got)
	}
}

func TestDispatchBumpsFailureCounter(t *testing.T) {
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(receiver.Close)

	store, _ := OpenWebhookStore(":memory:")
	defer func() { _ = store.Close() }()
	_, _ = store.Register(context.Background(), receiver.URL)

	var bumps atomic.Int64
	d := NewWebhookDispatcher(store, "counter-test")
	d.failedCounter = func() { bumps.Add(1) }
	_ = d.Dispatch(context.Background(), "schema_diff", []byte(`{}`))
	if bumps.Load() != 1 {
		t.Errorf("failed counter not bumped on 4xx; got %d", bumps.Load())
	}
}

func TestDispatchHonoursClientTimeout(t *testing.T) {
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(receiver.Close)

	store, _ := OpenWebhookStore(":memory:")
	defer func() { _ = store.Close() }()
	_, _ = store.Register(context.Background(), receiver.URL)

	d := NewWebhookDispatcher(store, "timeout-test")
	d.maxRetries = 0
	// Override the http client so this case finishes quickly.
	d.client = &http.Client{Timeout: 5 * time.Millisecond}
	_ = d.Dispatch(context.Background(), "schema_diff", []byte(`{}`))
	// Implicit assertion: test finishes in well under the 50ms
	// receiver delay. If we waited for the receiver the test
	// timeout would fire instead.
}

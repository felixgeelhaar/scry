package runtime

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/felixgeelhaar/scry/internal/obs"
)

// WebhookHTTPHeaderSignature is the header carrying the HMAC-SHA256
// hex of the body signed with the registration secret. Receivers
// verify via:
//
//	hmac.New(sha256.New, secret).Write(body)
//	want := hex.EncodeToString(mac.Sum(nil))
//	ok := hmac.Equal([]byte(want), []byte(r.Header.Get("X-Scry-Signature")))
const WebhookHTTPHeaderSignature = "X-Scry-Signature"

// WebhookHTTPHeaderEvent identifies the event type. Currently always
// "schema_diff" — future tasks may add more (auth_expired,
// rate_limited, etc.).
const WebhookHTTPHeaderEvent = "X-Scry-Event"

// WebhookHTTPClientTimeout caps per-delivery wall-clock. Bigger than
// scry's default upstream timeout because receivers may be on slow
// shared infrastructure (Slack incoming-webhook, Zapier, etc.).
const WebhookHTTPClientTimeout = 10 * time.Second

// WebhookDispatcher fans out an event to every registered webhook
// for one server. Each delivery retries up to maxRetries times with
// exponential backoff on 5xx + network errors; 4xx is recorded but
// not retried (the receiver intentionally rejected the event).
type WebhookDispatcher struct {
	store      *WebhookStore
	server     string
	maxRetries int
	client     *http.Client
	// failedCounter is bumped on every final-failure delivery so
	// operators can spot stuck receivers via scry's metrics. Nil
	// at construction; tests inject the metrics handle when they
	// care, production wires obs.Metrics().WebhookFailures.
	failedCounter func()
}

// NewWebhookDispatcher constructs a dispatcher tied to one store +
// the server name. The store's Forward() is what surfaces the
// signing secrets — caller MUST keep the dispatcher behind the
// runtime/manager boundary so MCP tools never get a handle.
func NewWebhookDispatcher(store *WebhookStore, server string) *WebhookDispatcher {
	return &WebhookDispatcher{
		store:      store,
		server:     server,
		maxRetries: 3,
		client:     &http.Client{Timeout: WebhookHTTPClientTimeout},
	}
}

// Dispatch fans out one event payload to every registered webhook
// for this server. Returns ErrNoRegistrations when no webhooks are
// configured — a sentinel the caller can compare with errors.Is
// without treating empty as failure (it isn't).
func (d *WebhookDispatcher) Dispatch(ctx context.Context, event string, body []byte) error {
	rows, err := d.store.Forward(ctx)
	if err != nil {
		return fmt.Errorf("forward webhooks: %w", err)
	}
	if len(rows) == 0 {
		return ErrNoRegistrations
	}
	for _, w := range rows {
		d.deliver(ctx, w, event, body)
	}
	return nil
}

// ErrNoRegistrations is returned by Dispatch when no webhooks are
// registered for the server. Not a fatal error — the caller usually
// continues without it.
var ErrNoRegistrations = errors.New("webhooks: no registrations")

// deliver POSTs the body to one webhook URL, retrying on retryable
// failures with exponential backoff. Final outcome is logged with
// the registration id + the masked URL host so operators can debug
// stuck deliveries without leaking full URLs into logs.
func (d *WebhookDispatcher) deliver(ctx context.Context, w Webhook, event string, body []byte) {
	mac := hmac.New(sha256.New, []byte(w.Secret))
	_, _ = mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	delay := 500 * time.Millisecond
	for attempt := 0; attempt <= d.maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", w.URL, bytes.NewReader(body))
		if err != nil {
			obs.L.Error().
				Str("event", "webhook.request_build_failed").
				Str("server", d.server).
				Int64("webhook_id", w.ID).
				Err(err).Send()
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(WebhookHTTPHeaderSignature, sig)
		req.Header.Set(WebhookHTTPHeaderEvent, event)
		req.Header.Set("User-Agent", "scry-webhook/0.7")
		resp, err := d.client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				obs.L.Info().
					Str("event", "webhook.delivered").
					Str("server", d.server).
					Int64("webhook_id", w.ID).
					Int("status", resp.StatusCode).
					Send()
				return
			}
			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				// Receiver rejected — no point retrying.
				obs.L.Warn().
					Str("event", "webhook.rejected_4xx").
					Str("server", d.server).
					Int64("webhook_id", w.ID).
					Int("status", resp.StatusCode).
					Send()
				d.bumpFailures()
				return
			}
			err = fmt.Errorf("http %d", resp.StatusCode)
		}
		if attempt == d.maxRetries {
			obs.L.Error().
				Str("event", "webhook.gave_up").
				Str("server", d.server).
				Int64("webhook_id", w.ID).
				Int("attempts", attempt+1).
				Err(err).Send()
			d.bumpFailures()
			return
		}
		obs.L.Warn().
			Str("event", "webhook.retry").
			Str("server", d.server).
			Int64("webhook_id", w.ID).
			Int("attempt", attempt+1).
			Err(err).Send()
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay *= 2
	}
}

func (d *WebhookDispatcher) bumpFailures() {
	if d.failedCounter != nil {
		d.failedCounter()
	}
}

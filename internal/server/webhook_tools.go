package server

import (
	"context"
	"encoding/json"
	"net/url"

	mcp "go.klarlabs.de/mcp"

	"github.com/felixgeelhaar/scry/internal/runtime"
)

// registerWebhookTools wires schema_diff_subscribe + schema_webhooks_list
// + schema_webhooks_remove. All admin-only — operators register
// outbound HTTP receivers that scry POSTs to on every non-empty
// schema diff. Secret is returned ONCE at registration time.
//
//nolint:unparam
func registerWebhookTools(srv *mcp.Server, mgr *runtime.Manager) error {
	type SubscribeInput struct {
		Server string `json:"server,omitempty" jsonschema:"description=upstream server name (omit when only one is configured)"`
		URL    string `json:"url" jsonschema:"required,description=HTTPS endpoint scry POSTs the diff JSON to on every refresh that produces changes"`
	}
	srv.Tool("schema_diff_subscribe").
		Description(descSchemaDiffSubscribe).
		Handler(func(ctx context.Context, in SubscribeInput) (string, error) {
			if denied := requireAdmin(ctx, "schema_diff_subscribe"); denied != "" {
				return denied, nil
			}
			entry, errResp := resolveServer(in.Server, mgr)
			if errResp != "" {
				return errResp, nil
			}
			if entry.Webhooks == nil {
				return renderError("webhooks_unavailable",
					"this server doesn't have a webhook store wired (older entry pre-v0.7); restart scry to populate it"), nil
			}
			// Validate the URL up front so the operator gets an
			// immediate signal instead of discovering the typo
			// when the next refresh tries to POST and fails.
			if u, err := url.Parse(in.URL); err != nil || u.Scheme == "" || u.Host == "" {
				return renderError("invalid_url",
					"webhook url must be a fully-qualified https:// (or http:// for testing) URL"), nil
			}
			w, err := entry.Webhooks.Register(ctx, in.URL)
			if err != nil {
				return renderError("register_failed", err.Error()), nil
			}
			enc, _ := json.MarshalIndent(map[string]any{
				"id":         w.ID,
				"url":        w.URL,
				"secret":     w.Secret,
				"created_at": w.CreatedAt,
				"hint":       "store this secret now — scry only returns it once. Receivers verify with HMAC-SHA256(secret, body) against the X-Scry-Signature header.",
			}, "", "  ")
			return string(enc), nil
		})

	type ListInput struct {
		Server string `json:"server,omitempty"`
	}
	srv.Tool("schema_webhooks_list").
		Description(descSchemaWebhooksList).
		Handler(func(ctx context.Context, in ListInput) (string, error) {
			if denied := requireAdmin(ctx, "schema_webhooks_list"); denied != "" {
				return denied, nil
			}
			entry, errResp := resolveServer(in.Server, mgr)
			if errResp != "" {
				return errResp, nil
			}
			if entry.Webhooks == nil {
				return renderError("webhooks_unavailable",
					"this server doesn't have a webhook store wired"), nil
			}
			rows, err := entry.Webhooks.List(ctx)
			if err != nil {
				return renderError("list_failed", err.Error()), nil
			}
			enc, _ := json.MarshalIndent(map[string]any{
				"server":   entry.Name,
				"webhooks": rows,
			}, "", "  ")
			return string(enc), nil
		})

	type RemoveInput struct {
		Server string `json:"server,omitempty"`
		ID     int64  `json:"id" jsonschema:"required,description=registration id returned by schema_diff_subscribe / schema_webhooks_list"`
	}
	srv.Tool("schema_webhooks_remove").
		Description(descSchemaWebhooksRemove).
		Handler(func(ctx context.Context, in RemoveInput) (string, error) {
			if denied := requireAdmin(ctx, "schema_webhooks_remove"); denied != "" {
				return denied, nil
			}
			entry, errResp := resolveServer(in.Server, mgr)
			if errResp != "" {
				return errResp, nil
			}
			if entry.Webhooks == nil {
				return renderError("webhooks_unavailable",
					"this server doesn't have a webhook store wired"), nil
			}
			if err := entry.Webhooks.Remove(ctx, in.ID); err != nil {
				return renderError("not_found",
					"no webhook with that id — call schema_webhooks_list to enumerate"), nil
			}
			enc, _ := json.Marshal(map[string]any{
				"removed_id": in.ID,
				"server":     entry.Name,
			})
			return string(enc), nil
		})
	return nil
}

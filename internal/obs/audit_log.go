package obs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/log/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

const auditLoggerName = "scry.audit"

// InitAuditLog sets up the global OTel log provider based on the
// OTEL_LOGS_EXPORTER env var:
//
//	none | unset → no-op provider (default; audit log shipping off)
//	otlp         → OTLP/HTTP exporter to OTEL_EXPORTER_OTLP_ENDPOINT
//	stdout       → JSON-encoded log records to stderr (dev only)
//
// Falls back to OTEL_TRACES_EXPORTER for the "ship every signal to
// the same place" convenience case — matches how InitMeter chains
// over the same fallback.
//
// Returns a shutdown func operators defer so buffered records flush
// on graceful exit.
func InitAuditLog(ctx context.Context) (shutdown func(context.Context) error, err error) {
	kind := strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_LOGS_EXPORTER")))
	if kind == "" {
		kind = strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_TRACES_EXPORTER")))
	}
	if kind == "" || kind == "none" {
		global.SetLoggerProvider(noop.NewLoggerProvider())
		return func(context.Context) error { return nil }, nil
	}

	exp, err := buildLogExporter(ctx, kind)
	if err != nil {
		return nil, fmt.Errorf("obs: build log exporter: %w", err)
	}
	res, err := buildResource(ctx)
	if err != nil {
		return nil, fmt.Errorf("obs: build resource: %w", err)
	}
	provider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
		sdklog.WithResource(res),
	)
	global.SetLoggerProvider(provider)
	return provider.Shutdown, nil
}

// AuditEvent is the wire shape we publish for each evidence record.
// Kept narrow on purpose: hashes only, never raw query/response
// bodies — the audit pipeline must be safe to ship to a third-party
// SIEM without leaking sensitive payloads.
type AuditEvent struct {
	Timestamp    time.Time
	Session      string
	Server       string
	Effect       string
	Outcome      string
	Complexity   int
	QueryHash    string
	ResponseHash string
	ChainHash    string
}

// EmitAuditEvent publishes one record through the global OTel
// logger provider. Cheap when the provider is no-op (the default);
// callers don't need to gate on env vars.
//
// Body is a short human-readable summary so a console-style sink
// renders something useful out of the box; the structured payload
// lives in attributes.
func EmitAuditEvent(ev AuditEvent) {
	lg := global.GetLoggerProvider().Logger(auditLoggerName)
	var rec log.Record
	if !ev.Timestamp.IsZero() {
		rec.SetTimestamp(ev.Timestamp)
	}
	rec.SetObservedTimestamp(time.Now())
	rec.SetSeverity(log.SeverityInfo)
	rec.SetSeverityText("INFO")
	rec.SetEventName("scry.audit.execute")
	rec.SetBody(log.StringValue(fmt.Sprintf(
		"scry.audit %s %s %s effect=%s outcome=%s",
		ev.Session, ev.Server, ev.QueryHash[:min(8, len(ev.QueryHash))],
		ev.Effect, ev.Outcome,
	)))
	rec.AddAttributes(
		log.String("session", ev.Session),
		log.String("server", ev.Server),
		log.String("effect", ev.Effect),
		log.String("outcome", ev.Outcome),
		log.Int("complexity", ev.Complexity),
		log.String("query_hash", ev.QueryHash),
		log.String("response_hash", ev.ResponseHash),
		log.String("chain_hash", ev.ChainHash),
	)
	lg.Emit(context.Background(), rec)
}

func buildLogExporter(ctx context.Context, kind string) (sdklog.Exporter, error) {
	switch kind {
	case "otlp", "otlp_http", "otlphttp":
		return otlploghttp.New(ctx)
	case "stdout":
		return stdoutlog.New(stdoutlog.WithPrettyPrint())
	default:
		return nil, fmt.Errorf("unknown log exporter %q (want none|otlp|stdout)", kind)
	}
}

// Avoid the otel/log import getting pruned by goimports when a
// future refactor briefly drops the only direct reference.
var _ = errors.New

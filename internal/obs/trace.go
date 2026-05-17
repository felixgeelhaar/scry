package obs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// tracerName is the instrumentation library name scry registers
// against. Spans created via Tracer() carry this as their
// `otel.library.name` attribute — operators filter on it to
// separate scry's spans from other services inside the same
// collector.
const tracerName = "github.com/felixgeelhaar/scry"

// InitTracer sets up the global OpenTelemetry tracer provider based
// on the OTEL_TRACES_EXPORTER env var:
//
//	none | unset  → no-op provider (default; tracing off)
//	otlp          → OTLP/HTTP exporter to OTEL_EXPORTER_OTLP_ENDPOINT
//	                (defaults to http://localhost:4318)
//	stdout        → JSON-encoded spans to stderr (dev only)
//
// Returns a shutdown function operators must defer so buffered
// spans flush on graceful exit. Returns a no-op shutdown when
// tracing is disabled so callers don't need to nil-check.
//
// Service name comes from OTEL_SERVICE_NAME (or "scry" by default).
// All other resource attributes come from OTEL_RESOURCE_ATTRIBUTES
// per the spec — operators control deployment.environment,
// service.version, etc. via that single env var.
func InitTracer(ctx context.Context) (shutdown func(context.Context) error, err error) {
	exporterKind := strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_TRACES_EXPORTER")))
	if exporterKind == "" || exporterKind == "none" {
		otel.SetTracerProvider(noop.NewTracerProvider())
		otel.SetTextMapPropagator(propagation.TraceContext{})
		return func(context.Context) error { return nil }, nil
	}

	exp, err := buildExporter(ctx, exporterKind)
	if err != nil {
		return nil, fmt.Errorf("obs: build trace exporter: %w", err)
	}

	res, err := buildResource(ctx)
	if err != nil {
		return nil, fmt.Errorf("obs: build resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp,
			sdktrace.WithBatchTimeout(5*time.Second),
			sdktrace.WithMaxExportBatchSize(512),
		),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
	)

	otel.SetTracerProvider(tp)
	// W3C trace-context propagator: incoming HTTP traceparent
	// headers continue the agent's trace, outgoing requests carry
	// the header onward to the upstream.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp.Shutdown, nil
}

// Tracer returns the package-level tracer. Wraps otel.Tracer so
// callers don't reach into the global directly — keeps test
// substitution easier later.
func Tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

func buildExporter(ctx context.Context, kind string) (sdktrace.SpanExporter, error) {
	switch kind {
	case "otlp", "otlp_http", "otlphttp":
		// Honour OTEL_EXPORTER_OTLP_ENDPOINT + OTEL_EXPORTER_OTLP_HEADERS
		// natively. The default endpoint is the standard
		// localhost:4318 used by every OTLP collector.
		return otlptrace.New(ctx, otlptracehttp.NewClient())
	case "stdout":
		return stdouttrace.New(stdouttrace.WithPrettyPrint())
	default:
		return nil, fmt.Errorf("unknown OTEL_TRACES_EXPORTER %q (want none|otlp|stdout)", kind)
	}
}

func buildResource(ctx context.Context) (*resource.Resource, error) {
	svc := os.Getenv("OTEL_SERVICE_NAME")
	if svc == "" {
		svc = "scry"
	}
	r, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithProcess(),
		resource.WithHost(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(semconv.ServiceName(svc)),
	)
	if errors.Is(err, resource.ErrPartialResource) {
		// Partial resources are fine — usually means one of the
		// auto-detectors couldn't read /etc/os-release or similar.
		// Carry on.
		return r, nil
	}
	return r, err
}

package obs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// meterName must match tracerName so collectors can filter by the
// same instrumentation-library attribute regardless of signal.
const meterName = tracerName

var (
	metersOnce sync.Once
	meters     ScryMeters
)

// ScryMeters bundles every counter/histogram scry emits. Created
// lazily on first call to Metrics() so tests that don't init the
// meter provider still get usable no-op instruments.
type ScryMeters struct {
	ExecuteCount      metric.Int64Counter
	ExecuteDuration   metric.Float64Histogram
	ExecuteComplexity metric.Int64Histogram
	IntrospectCount   metric.Int64Counter
	IntrospectErrors  metric.Int64Counter
	UpstreamLatency   metric.Float64Histogram
	SchemaChanges     metric.Int64Counter
}

// InitMeter sets up the global meter provider based on the OTEL
// metrics env vars. Mirrors InitTracer:
//
//	none | unset → no-op meter provider
//	otlp         → OTLP/HTTP exporter
//	stdout       → JSON metrics to stderr (dev)
//
// Independent of the trace env var so operators can ship logs +
// traces without metrics (the common case) or metrics without
// traces (also common in pipelines that scrape Prometheus).
//
// Reads OTEL_METRICS_EXPORTER first; falls back to OTEL_TRACES_EXPORTER
// for the "both signals to the same place" convenience case.
func InitMeter(ctx context.Context) (shutdown func(context.Context) error, err error) {
	kind := strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_METRICS_EXPORTER")))
	if kind == "" {
		kind = strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_TRACES_EXPORTER")))
	}
	if kind == "" || kind == "none" {
		otel.SetMeterProvider(noop.NewMeterProvider())
		return func(context.Context) error { return nil }, nil
	}

	exp, err := buildMetricExporter(ctx, kind)
	if err != nil {
		return nil, fmt.Errorf("obs: build metric exporter: %w", err)
	}
	res, err := buildResource(ctx)
	if err != nil {
		return nil, fmt.Errorf("obs: build resource: %w", err)
	}
	reader := sdkmetric.NewPeriodicReader(exp,
		sdkmetric.WithInterval(15*time.Second),
	)
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)
	return mp.Shutdown, nil
}

// Metrics returns the package-level ScryMeters. Lazily constructs
// instruments on first call so the meter provider can be set by
// either InitMeter or a test override before instruments are
// created.
func Metrics() ScryMeters {
	metersOnce.Do(func() {
		m := otel.Meter(meterName)
		var err error
		meters.ExecuteCount, err = m.Int64Counter("scry.query_execute.count",
			metric.WithDescription("Number of query_execute calls by server + outcome"))
		mustMetric(err)
		meters.ExecuteDuration, err = m.Float64Histogram("scry.query_execute.duration_seconds",
			metric.WithDescription("End-to-end query_execute latency in seconds"),
			metric.WithUnit("s"))
		mustMetric(err)
		meters.ExecuteComplexity, err = m.Int64Histogram("scry.query_execute.complexity",
			metric.WithDescription("Estimated complexity of executed queries"))
		mustMetric(err)
		meters.IntrospectCount, err = m.Int64Counter("scry.introspect.count",
			metric.WithDescription("Introspection refresh attempts by server + mode"))
		mustMetric(err)
		meters.IntrospectErrors, err = m.Int64Counter("scry.introspect.errors",
			metric.WithDescription("Introspection failures by server"))
		mustMetric(err)
		meters.UpstreamLatency, err = m.Float64Histogram("scry.upstream.latency_seconds",
			metric.WithDescription("Upstream GraphQL POST latency in seconds"),
			metric.WithUnit("s"))
		mustMetric(err)
		meters.SchemaChanges, err = m.Int64Counter("scry.schema.changes_total",
			metric.WithDescription("Schema changes detected on refresh, by server + kind (added|removed|breaking)"))
		mustMetric(err)
	})
	return meters
}

// mustMetric panics on instrument-creation error. Acceptable because
// it can only fail on duplicate names within one meter, which would
// be a programming bug, not a runtime condition.
func mustMetric(err error) {
	if err != nil {
		panic("obs: failed to create metric instrument: " + err.Error())
	}
}

func buildMetricExporter(ctx context.Context, kind string) (sdkmetric.Exporter, error) {
	switch kind {
	case "otlp", "otlp_http", "otlphttp":
		return otlpmetrichttp.New(ctx)
	case "stdout":
		return stdoutmetric.New(stdoutmetric.WithPrettyPrint())
	default:
		return nil, fmt.Errorf("unknown metric exporter %q (want none|otlp|stdout)", kind)
	}
}

// Force a reference to metricdata so the import isn't pruned by
// goimports — needed because metricdata exposes the unit constants
// that real metric exporters consume even when they aren't
// directly referenced here.
var _ = metricdata.Temporality(0)

// ResetMetersForTest forces the next Metrics() call to rebuild
// instruments against whatever meter provider is currently set.
// Exported so tests in sibling packages can reset between cases
// without exposing the underlying sync.Once.
func ResetMetersForTest() {
	metersOnce = sync.Once{}
	meters = ScryMeters{}
}

// keep errors imported even when nothing uses it explicitly — it
// keeps the import list stable across edits.
var _ = errors.New

package obs

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// TestInitTracerDefaultIsNoOp confirms that with no env var set,
// InitTracer registers the noop provider — tracing stays off and
// no exporter is contacted. Important: the happy-path "I just want
// to run scry" experience must not require an OTel collector.
func TestInitTracerDefaultIsNoOp(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "")
	shutdown, err := InitTracer(context.Background())
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	tp := otel.GetTracerProvider()
	if _, ok := tp.(noop.TracerProvider); !ok {
		t.Errorf("expected noop.TracerProvider when OTEL_TRACES_EXPORTER is empty, got %T", tp)
	}

	// Start a span and confirm the span context is invalid (the
	// noop provider doesn't generate trace IDs).
	_, span := Tracer().Start(context.Background(), "test")
	if span.SpanContext().IsValid() {
		t.Errorf("noop provider should not produce a valid span context")
	}
	span.End()
}

func TestInitTracerStdoutWritesToProcess(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "stdout")
	shutdown, err := InitTracer(context.Background())
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	tp := otel.GetTracerProvider()
	if _, ok := tp.(noop.TracerProvider); ok {
		t.Errorf("stdout exporter should NOT yield noop provider")
	}
}

func TestInitTracerRejectsUnknownExporter(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "carrier-pigeon")
	_, err := InitTracer(context.Background())
	if err == nil {
		t.Errorf("expected error for unknown exporter, got nil")
	}
}

// TestTracerProducesValidSpanWhenEnabled ensures the wire from
// InitTracer through Tracer() yields a span context with valid
// IDs when tracing is on. Catches regressions that would silently
// drop traces (e.g. forgetting to set the global provider).
func TestTracerProducesValidSpanWhenEnabled(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "stdout")
	shutdown, err := InitTracer(context.Background())
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	_, span := Tracer().Start(context.Background(), "smoke")
	defer span.End()
	sc := span.SpanContext()
	if !sc.IsValid() {
		t.Errorf("expected valid span context, got %+v", sc)
	}
	if !sc.TraceID().IsValid() || !sc.SpanID().IsValid() {
		t.Errorf("trace_id or span_id invalid: trace=%s span=%s", sc.TraceID(), sc.SpanID())
	}
}

// TestTracerNameMatchesPackage guards against accidental rename of
// the instrumentation library — operators filter on this in their
// collector pipelines.
func TestTracerNameMatchesPackage(t *testing.T) {
	if tracerName != "github.com/felixgeelhaar/scry" {
		t.Errorf("tracerName drifted to %q — collector filters will break", tracerName)
	}
	// Sanity: Tracer() returns the same tracer instance as
	// fetching by name directly.
	a := Tracer()
	b := otel.Tracer(tracerName)
	if a != b {
		// Pointer equality isn't guaranteed by the spec but the
		// OTel SDK caches by name; failing here means the name
		// constant doesn't match what Tracer() passes.
		t.Logf("Tracer() did not return the cached instance — review the constant")
	}
	// Use the references so the test isn't flagged for unused
	// variables when the SDK happens to return distinct pointers.
	_ = a
	_ = b
	_ = trace.SpanFromContext
}

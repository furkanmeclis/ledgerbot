package telemetry

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"
)

func TestSetupNoopAndPropagation(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown, err := Setup(context.Background(), "test", "dev")
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown(context.Background()) //nolint:errcheck
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1, 2, 3}, SpanID: trace.SpanID{4, 5, 6}, TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	carrier := Inject(ctx)
	if carrier["traceparent"] == "" {
		t.Fatalf("traceparent not injected: %v", carrier)
	}
	got := trace.SpanContextFromContext(Extract(context.Background(), carrier))
	if got.TraceID() != sc.TraceID() {
		t.Fatalf("trace id lost: %v", got.TraceID())
	}
	if Inject(context.Background()) != nil {
		t.Fatal("empty context must inject nothing")
	}
}

func TestSetupWithExporter(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")
	shutdown, err := Setup(context.Background(), "test", "dev")
	if err != nil {
		t.Fatalf("tracing setup with an endpoint: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = shutdown(ctx) // nothing listens on the endpoint; only setup matters here
}

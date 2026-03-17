package observability_test

import (
	"context"
	"testing"

	"github.com/jesus-mata/tanugate/internal/config"
	"github.com/jesus-mata/tanugate/internal/observability"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestNewTracerProvider_Disabled(t *testing.T) {
	cfg := config.TracingConfig{
		Enabled:     false,
		Exporter:    "stdout",
		SampleRate:  1.0,
		ServiceName: "test",
	}

	tp, shutdown, err := observability.NewTracerProvider(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tp == nil {
		t.Fatal("expected non-nil TracerProvider")
	}
	if shutdown == nil {
		t.Fatal("expected non-nil shutdown function")
	}

	// The disabled provider should use NeverSample — spans should not be recorded.
	tracer := tp.Tracer("test")
	_, span := tracer.Start(context.Background(), "test-span")
	if span.SpanContext().IsSampled() {
		t.Error("expected span to NOT be sampled when tracing is disabled")
	}
	span.End()

	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown error: %v", err)
	}
}

func TestNewTracerProvider_StdoutEnabled(t *testing.T) {
	cfg := config.TracingConfig{
		Enabled:     true,
		Exporter:    "stdout",
		SampleRate:  1.0,
		ServiceName: "test-service",
	}

	tp, shutdown, err := observability.NewTracerProvider(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tp == nil {
		t.Fatal("expected non-nil TracerProvider")
	}

	// Verify we can create spans.
	tracer := tp.Tracer("test")
	_, span := tracer.Start(context.Background(), "test-span")
	if !span.SpanContext().IsSampled() {
		t.Error("expected span to be sampled when tracing is enabled with rate 1.0")
	}
	span.End()

	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown error: %v", err)
	}
}

func TestNewTracerProvider_UnsupportedExporter(t *testing.T) {
	cfg := config.TracingConfig{
		Enabled:     true,
		Exporter:    "otlp",
		SampleRate:  1.0,
		ServiceName: "test",
	}

	_, _, err := observability.NewTracerProvider(cfg)
	if err == nil {
		t.Fatal("expected error for unsupported exporter")
	}
}

func TestNewTracerProvider_SampleRateZero(t *testing.T) {
	cfg := config.TracingConfig{
		Enabled:     true,
		Exporter:    "stdout",
		SampleRate:  0,
		ServiceName: "test",
	}

	tp, shutdown, err := observability.NewTracerProvider(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	tracer := tp.Tracer("test")
	_, span := tracer.Start(context.Background(), "test-span")
	if span.SpanContext().IsSampled() {
		t.Error("expected span to NOT be sampled with rate 0")
	}
	span.End()
}

func TestNewTracerProvider_SampleRateFractional(t *testing.T) {
	cfg := config.TracingConfig{
		Enabled:     true,
		Exporter:    "stdout",
		SampleRate:  0.5,
		ServiceName: "test",
	}

	tp, shutdown, err := observability.NewTracerProvider(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	if tp == nil {
		t.Fatal("expected non-nil TracerProvider")
	}
}

func TestNewTracerProvider_ShutdownFlushesSpans(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	tracer := tp.Tracer("test")
	_, span := tracer.Start(context.Background(), "flush-test-span")
	span.End()

	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("expected at least one span after span.End()")
	}

	found := false
	for _, s := range spans {
		if s.Name == "flush-test-span" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected to find 'flush-test-span' in exported spans")
	}

	if err := tp.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown error: %v", err)
	}
}

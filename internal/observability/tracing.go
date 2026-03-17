package observability

import (
	"context"
	"fmt"

	"github.com/jesus-mata/tanugate/internal/config"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// NewTracerProvider creates an OpenTelemetry TracerProvider configured
// according to the given TracingConfig. It returns the provider, a shutdown
// function that flushes pending spans, and an error.
//
// This tracer-bullet slice supports the "stdout" exporter only. The "otlp"
// exporter will be added in a follow-up issue.
func NewTracerProvider(cfg config.TracingConfig) (*sdktrace.TracerProvider, func(context.Context) error, error) {
	if !cfg.Enabled {
		// Return a no-op provider that never samples.
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithSampler(sdktrace.NeverSample()),
		)
		return tp, tp.Shutdown, nil
	}

	res, err := resource.New(context.Background(),
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
		),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("creating tracing resource: %w", err)
	}

	var exporter sdktrace.SpanExporter
	switch cfg.Exporter {
	case "stdout":
		exp, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			return nil, nil, fmt.Errorf("creating stdout exporter: %w", err)
		}
		exporter = exp
	default:
		return nil, nil, fmt.Errorf("unsupported tracing exporter %q (this build supports \"stdout\" only)", cfg.Exporter)
	}

	var sampler sdktrace.Sampler
	switch {
	case cfg.SampleRate >= 1.0:
		sampler = sdktrace.AlwaysSample()
	case cfg.SampleRate <= 0:
		sampler = sdktrace.NeverSample()
	default:
		sampler = sdktrace.TraceIDRatioBased(cfg.SampleRate)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	)

	return tp, tp.Shutdown, nil
}

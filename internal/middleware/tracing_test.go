package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jesus-mata/tanugate/internal/middleware"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

func newTestTracerProvider(t *testing.T) (*sdktrace.TracerProvider, *tracetest.InMemoryExporter) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return tp, exporter
}

func TestTracing_CreatesServerSpan(t *testing.T) {
	tp, exporter := newTestTracerProvider(t)

	handler := middleware.Tracing(tp)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}

	span := spans[0]
	if span.Name != "GET /api/users" {
		t.Errorf("span name = %q, want %q", span.Name, "GET /api/users")
	}
	if span.SpanKind != trace.SpanKindServer {
		t.Errorf("span kind = %v, want %v", span.SpanKind, trace.SpanKindServer)
	}
}

func TestTracing_SetsHTTPAttributes(t *testing.T) {
	tp, exporter := newTestTracerProvider(t)

	handler := middleware.Tracing(tp)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/items", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}

	span := spans[0]

	foundMethod := false
	foundStatusCode := false
	for _, attr := range span.Attributes {
		if attr.Key == semconv.HTTPRequestMethodKey && attr.Value.AsString() == "POST" {
			foundMethod = true
		}
		if attr.Key == semconv.HTTPResponseStatusCodeKey && attr.Value.AsInt64() == 201 {
			foundStatusCode = true
		}
	}
	if !foundMethod {
		t.Error("expected http.request.method=POST attribute on span")
	}
	if !foundStatusCode {
		t.Error("expected http.response.status_code=201 attribute on span")
	}
}

func TestTracing_ExtractsTraceparent(t *testing.T) {
	tp, exporter := newTestTracerProvider(t)

	handler := middleware.Tracing(tp)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		spanCtx := trace.SpanFromContext(r.Context()).SpanContext()
		if !spanCtx.IsValid() {
			t.Error("expected valid span context in request")
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}

	traceID := spans[0].SpanContext.TraceID().String()
	if traceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace ID = %q, want %q", traceID, "4bf92f3577b34da6a3ce929d0e0e4736")
	}

	parentSpanID := spans[0].Parent.SpanID().String()
	if parentSpanID != "00f067aa0ba902b7" {
		t.Errorf("parent span ID = %q, want %q", parentSpanID, "00f067aa0ba902b7")
	}
}

func TestTracing_NoTraceparent_GeneratesNewTrace(t *testing.T) {
	tp, exporter := newTestTracerProvider(t)

	handler := middleware.Tracing(tp)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}

	if !spans[0].SpanContext.TraceID().IsValid() {
		t.Error("expected valid trace ID when no traceparent is provided")
	}
	if spans[0].Parent.SpanID().IsValid() {
		t.Error("expected no parent span when no traceparent is provided")
	}
}

func TestTracing_PropagatesContextToInnerHandler(t *testing.T) {
	tp, _ := newTestTracerProvider(t)

	var innerSpanCtx trace.SpanContext
	handler := middleware.Tracing(tp)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerSpanCtx = trace.SpanFromContext(r.Context()).SpanContext()
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !innerSpanCtx.IsValid() {
		t.Error("expected valid span context propagated to inner handler")
	}
	if !innerSpanCtx.IsSampled() {
		t.Error("expected span to be sampled")
	}
}

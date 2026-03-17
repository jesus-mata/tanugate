package middleware

import (
	"fmt"
	"net/http"

	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// Tracing returns a middleware that creates a server span for each request.
// It extracts incoming W3C trace context (traceparent / tracestate headers)
// and sets standard HTTP semantic convention attributes on the span.
//
// This should be the outermost middleware in the chain so that the span
// covers the full request lifecycle including all other middlewares.
func Tracing(tp trace.TracerProvider) Middleware {
	tracer := tp.Tracer("github.com/jesus-mata/tanugate/middleware")
	propagator := propagation.TraceContext{}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extract W3C trace context from incoming headers.
			ctx := propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))

			spanName := fmt.Sprintf("%s %s", r.Method, r.URL.Path)
			ctx, span := tracer.Start(ctx, spanName,
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					semconv.HTTPRequestMethodKey.String(r.Method),
					semconv.URLFull(r.URL.String()),
				),
			)
			defer span.End()

			ww := WrapResponseWriter(w)
			next.ServeHTTP(ww, r.WithContext(ctx))

			span.SetAttributes(
				semconv.HTTPResponseStatusCode(ww.StatusCode()),
			)
		})
	}
}

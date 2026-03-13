package transform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jesus-mata/tanugate/internal/config"
	"github.com/jesus-mata/tanugate/internal/middleware"
	"github.com/jesus-mata/tanugate/internal/router"
)

type startTimeKey struct{}

var varPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

const defaultMaxTransformBodySize = 10 << 20 // 10 MB

func effectiveMaxBody(configured int64) int64 {
	if configured > 0 {
		return configured
	}
	return defaultMaxTransformBodySize
}

func writeJSON(w http.ResponseWriter, status int, errCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"error":   errCode,
		"message": message,
	})
}

// resolveVariables replaces ${...} placeholders in s with their resolved values.
func resolveVariables(s string, r *http.Request, latencyMs *int64) string {
	return varPattern.ReplaceAllStringFunc(s, func(match string) string {
		name := varPattern.FindStringSubmatch(match)[1]
		switch name {
		case "request_id":
			return middleware.RequestIDFromContext(r.Context())
		case "client_ip":
			return clientIP(r)
		case "timestamp_iso":
			return time.Now().UTC().Format(time.RFC3339)
		case "timestamp_unix":
			return strconv.FormatInt(time.Now().Unix(), 10)
		case "latency_ms":
			if latencyMs != nil {
				return strconv.FormatInt(*latencyMs, 10)
			}
			return "0"
		case "route_name":
			if mr := router.RouteFromContext(r.Context()); mr != nil {
				return mr.Config.Name
			}
			return ""
		case "method":
			return r.Method
		case "path":
			return r.URL.Path
		default:
			return match // unknown variable left unchanged
		}
	})
}

// clientIP extracts the client IP from X-Forwarded-For (first entry) or RemoteAddr.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if ip, _, ok := strings.Cut(xff, ","); ok {
			return strings.TrimSpace(ip)
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isJSON checks if the Content-Type header indicates JSON.
func isJSON(ct string) bool {
	ct, _, _ = strings.Cut(ct, ";")
	return strings.TrimSpace(ct) == "application/json"
}

// transformHeaders applies remove, rename, add operations to h in that order.
func transformHeaders(h http.Header, cfg *config.HeaderTransform, r *http.Request, latencyMs *int64) {
	if cfg == nil {
		return
	}
	for _, name := range cfg.Remove {
		h.Del(name)
	}
	for old, newName := range cfg.Rename {
		vals := h.Values(old)
		if len(vals) == 0 {
			continue
		}
		h.Del(old)
		for _, v := range vals {
			h.Add(newName, v)
		}
	}
	for name, value := range cfg.Add {
		h.Set(name, resolveVariables(value, r, latencyMs))
	}
}

// transformBody applies strip, rename, inject operations to a JSON body.
// Non-JSON or non-object bodies are returned unchanged.
func transformBody(body []byte, cfg *config.BodyTransform, r *http.Request, latencyMs *int64) ([]byte, error) {
	if cfg == nil || len(body) == 0 {
		return body, nil
	}

	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		slog.Debug("body transform: not a JSON object, passing through", "error", err)
		return body, nil
	}

	for _, field := range cfg.StripFields {
		delete(data, field)
	}
	for old, newKey := range cfg.RenameKeys {
		if val, ok := data[old]; ok {
			data[newKey] = val
			delete(data, old)
		}
	}
	for key, value := range cfg.InjectFields {
		if s, ok := value.(string); ok {
			data[key] = resolveVariables(s, r, latencyMs)
		} else {
			data[key] = value
		}
	}

	return json.Marshal(data)
}

// RequestTransform returns a middleware that transforms request headers and body
// based on cfg. It always stores the start time in context for latency calculation.
func RequestTransform(cfg *config.DirectionTransform, maxBodySize int64) middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), startTimeKey{}, time.Now())
			r = r.WithContext(ctx)

			if cfg == nil {
				next.ServeHTTP(w, r)
				return
			}

			transformHeaders(r.Header, cfg.Headers, r, nil)

			if cfg.Body != nil && isJSON(r.Header.Get("Content-Type")) {
				limit := effectiveMaxBody(maxBodySize)
				body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
				r.Body.Close()
				if err != nil {
					writeJSON(w, http.StatusBadGateway, "bad_gateway", "failed to read request body")
					return
				}
				if int64(len(body)) > limit {
					writeJSON(w, http.StatusRequestEntityTooLarge, "request_too_large",
						"request body exceeds transform size limit")
					return
				}
				if len(body) > 0 {
					transformed, terr := transformBody(body, cfg.Body, r, nil)
					if terr == nil {
						r.Body = io.NopCloser(bytes.NewReader(transformed))
						r.ContentLength = int64(len(transformed))
						r.Header.Set("Content-Length", strconv.Itoa(len(transformed)))
					} else {
						r.Body = io.NopCloser(bytes.NewReader(body))
					}
				} else {
					r.Body = io.NopCloser(bytes.NewReader(body))
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

// ResponseTransform returns a middleware that buffers the upstream response
// and applies header/body transforms before flushing to the client.
func ResponseTransform(cfg *config.DirectionTransform, maxBodySize int64) middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg == nil {
				next.ServeHTTP(w, r)
				return
			}

			limit := effectiveMaxBody(maxBodySize)
			buf := &bufferingResponseWriter{
				headers:    make(http.Header),
				statusCode: http.StatusOK,
				maxBody:    limit,
			}

			next.ServeHTTP(buf, r)

			if buf.exceeded {
				writeJSON(w, http.StatusBadGateway, "bad_gateway",
					"upstream response body exceeds transform size limit")
				return
			}

			// Compute latency.
			var latencyMs int64
			if start, ok := r.Context().Value(startTimeKey{}).(time.Time); ok {
				latencyMs = time.Since(start).Milliseconds()
			}

			transformHeaders(buf.headers, cfg.Headers, r, &latencyMs)

			body := buf.body.Bytes()
			if cfg.Body != nil && isJSON(buf.headers.Get("Content-Type")) && len(body) > 0 {
				transformed, err := transformBody(body, cfg.Body, r, &latencyMs)
				if err == nil {
					body = transformed
				}
			}

			// Remove Transfer-Encoding: chunked since we write full body.
			buf.headers.Del("Transfer-Encoding")
			buf.headers.Set("Content-Length", strconv.Itoa(len(body)))

			// Copy buffered headers to real writer.
			for k, vals := range buf.headers {
				for _, v := range vals {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(buf.statusCode)
			w.Write(body)
		})
	}
}

// StartTimeFromContext retrieves the request start time stored by RequestTransform.
func StartTimeFromContext(ctx context.Context) (time.Time, bool) {
	t, ok := ctx.Value(startTimeKey{}).(time.Time)
	return t, ok
}

// bufferingResponseWriter captures the entire response (headers, status, body)
// so that transforms can be applied before sending to the client.
type bufferingResponseWriter struct {
	headers     http.Header
	body        bytes.Buffer
	statusCode  int
	wroteHeader bool
	maxBody     int64
	exceeded    bool
}

func (w *bufferingResponseWriter) Header() http.Header {
	return w.headers
}

func (w *bufferingResponseWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.statusCode = code
}

func (w *bufferingResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.wroteHeader = true
	}
	if w.maxBody > 0 && int64(w.body.Len())+int64(len(b)) > w.maxBody {
		w.exceeded = true
		return 0, errors.New("response body exceeds transform size limit")
	}
	return w.body.Write(b)
}

// Flush implements http.Flusher. It is a no-op because the entire response
// is buffered until transforms are applied.
func (w *bufferingResponseWriter) Flush() {}

var _ http.Flusher = (*bufferingResponseWriter)(nil)

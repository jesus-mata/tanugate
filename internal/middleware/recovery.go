package middleware

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime"
)

func Recovery() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					stack := make([]byte, 4096)
					n := runtime.Stack(stack, false)
					slog.Error("panic recovered",
						"panic", rec,
						"stack", string(stack[:n]),
					)

					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]string{
						"error":   "internal_error",
						"message": "Internal server error",
					})
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}

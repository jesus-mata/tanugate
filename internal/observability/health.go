package observability

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/jesus-mata/tanugate/internal/config"
)

// HealthChecker is implemented by backends that can report their health.
type HealthChecker interface {
	HealthCheck(ctx context.Context) error
}

// HealthResponse is the JSON body returned by the health endpoint.
type HealthResponse struct {
	Status    string            `json:"status"`
	Timestamp string            `json:"timestamp"`
	Checks    map[string]string `json:"checks,omitempty"`
}

// HealthHandler returns an http.HandlerFunc that reports gateway liveness.
// The config is atomically loaded on each request so that hot-reloads are
// reflected without restarting. If the config uses a Redis rate-limit backend,
// the checker is exercised to determine if the gateway is degraded.
func HealthHandler(cfgPtr *atomic.Pointer[config.GatewayConfig], checker HealthChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := cfgPtr.Load()
		resp := HealthResponse{
			Status:    "up",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}

		if cfg.RateLimit.Backend == "redis" {
			resp.Checks = make(map[string]string)
			if checker == nil {
				resp.Checks["redis"] = "not_configured"
			} else {
				ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
				defer cancel()
				if err := checker.HealthCheck(ctx); err != nil {
					resp.Checks["redis"] = "down"
					resp.Status = "degraded"
				} else {
					resp.Checks["redis"] = "up"
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		statusCode := http.StatusOK
		if resp.Status == "degraded" {
			statusCode = http.StatusServiceUnavailable
		}
		w.WriteHeader(statusCode)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

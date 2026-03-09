package router

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/NextSolutionCUU/api-gateway/internal/config"
)

// matchedRouteKey is an unexported context key used to store the MatchedRoute.
type matchedRouteKey struct{}

// MatchedRoute holds the configuration and extracted path parameters for the
// route that matched an incoming request.
type MatchedRoute struct {
	Config     *config.RouteConfig
	PathParams map[string]string
}

// compiledRoute is the internal representation of a route after its regex has
// been compiled and its allowed methods have been indexed for fast lookup.
type compiledRoute struct {
	name    string
	regex   *regexp.Regexp
	methods map[string]bool // nil means all methods are allowed
	handler http.Handler
	config  *config.RouteConfig
}

// Router evaluates incoming requests against an ordered list of compiled
// routes and dispatches to the first matching handler.
type Router struct {
	routes []compiledRoute
}

// New compiles the provided route configurations and returns a ready-to-use
// Router. It panics if any route contains an invalid regex (fail-fast).
func New(configs []config.RouteConfig, handlers map[string]http.Handler) *Router {
	routes := make([]compiledRoute, 0, len(configs))

	for i := range configs {
		cfg := &configs[i]

		re := regexp.MustCompile(cfg.Match.PathRegex)

		var methods map[string]bool
		if len(cfg.Match.Methods) > 0 {
			methods = make(map[string]bool, len(cfg.Match.Methods))
			for _, m := range cfg.Match.Methods {
				methods[m] = true
			}
		}

		h := handlers[cfg.Name]

		routes = append(routes, compiledRoute{
			name:    cfg.Name,
			regex:   re,
			methods: methods,
			handler: h,
			config:  cfg,
		})
	}

	return &Router{routes: routes}
}

// ServeHTTP implements http.Handler. It evaluates routes in order and
// dispatches to the first match. If no route matches it responds with a
// 404 JSON error.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for i := range rt.routes {
		cr := &rt.routes[i]

		matches := cr.regex.FindStringSubmatch(r.URL.Path)
		if matches == nil {
			continue
		}

		if cr.methods != nil && !cr.methods[r.Method] {
			continue
		}

		// Extract named capture groups into PathParams.
		params := make(map[string]string)
		for j, name := range cr.regex.SubexpNames() {
			if j == 0 || name == "" {
				continue
			}
			params[name] = matches[j]
		}

		mr := &MatchedRoute{
			Config:     cr.config,
			PathParams: params,
		}

		ctx := context.WithValue(r.Context(), matchedRouteKey{}, mr)
		cr.handler.ServeHTTP(w, r.WithContext(ctx))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	json.NewEncoder(w).Encode(map[string]string{
		"error":   "not_found",
		"message": "No route matched",
	})
}

// RouteFromContext retrieves the MatchedRoute stored in ctx by the router.
// It returns nil if no route information is present.
func RouteFromContext(ctx context.Context) *MatchedRoute {
	mr, _ := ctx.Value(matchedRouteKey{}).(*MatchedRoute)
	return mr
}

// WithMatchedRoute returns a new context carrying the given MatchedRoute.
// This is intended for use in tests to inject route information without
// going through the full routing pipeline.
func WithMatchedRoute(ctx context.Context, mr *MatchedRoute) context.Context {
	return context.WithValue(ctx, matchedRouteKey{}, mr)
}

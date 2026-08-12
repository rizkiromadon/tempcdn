package httpserver

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rizkiromadon/tempcdn/internal/response"
)

type responseRecorder struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
}

func (rec *responseRecorder) WriteHeader(statusCode int) {
	if rec.wroteHeader {
		return
	}
	rec.wroteHeader = true
	rec.statusCode = statusCode
	rec.ResponseWriter.WriteHeader(statusCode)
}

func (rec *responseRecorder) Write(b []byte) (int, error) {
	if !rec.wroteHeader {
		rec.WriteHeader(http.StatusOK)
	}
	return rec.ResponseWriter.Write(b)
}

func RequestLogging(logger *slog.Logger, latency *prometheus.HistogramVec) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			startedAt := time.Now()
			recorder := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}

			next.ServeHTTP(recorder, r)

			elapsed := time.Since(startedAt)

			logger.Info("http_request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", recorder.statusCode,
				"latency_ms", elapsed.Milliseconds(),
			)

			if latency != nil {
				latency.WithLabelValues(r.URL.Path, r.Method).Observe(elapsed.Seconds())
			}
		})
	}
}

func Recovery(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recorder := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}
			defer func() {
				if recovered := recover(); recovered != nil {
					logger.Error("panic_recovered", "error", recovered, "path", r.URL.Path)

					if !recorder.wroteHeader {
						response.Error(recorder, http.StatusInternalServerError, "internal server error")
					}
				}
			}()
			next.ServeHTTP(recorder, r)
		})
	}
}

func CORS(allowedOrigin string, methods string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
			w.Header().Set("Access-Control-Allow-Methods", methods)
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Delete-Token, X-Metrics-Token, Authorization")
			w.Header().Set("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// noopHandler responds with an empty 200 body. CORS() already answers
// OPTIONS requests itself (see the short-circuit above), so noopHandler is
// only ever reached as the innermost handler behind a CORS-wrapped OPTIONS
// registration — it exists so route() has something harmless to wrap.
var noopHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

// routeRegistry tracks, per pattern, which methods have been registered via
// route() so far, so that when two different methods share the same
// pattern (e.g. GET and POST both on "/api-keys") the OPTIONS preflight for
// that pattern is registered exactly once, advertising every method
// registered on it — instead of chi panicking on a duplicate OPTIONS route.
type routeRegistry struct {
	methodsByPattern map[string][]string
}

func newRouteRegistry() *routeRegistry {
	return &routeRegistry{methodsByPattern: make(map[string][]string)}
}

// route registers a single JSON API endpoint on pattern for the given HTTP
// method, wraps it with a CORS middleware allowing exactly that method, and
// (re-)registers the OPTIONS preflight for pattern advertising every method
// registered on it so far.
//
// This is the one-liner every new resource should use instead of hand-writing
// a With(...).Get/Post/...(...) call plus a separate Options(...) call: adding
// a new endpoint becomes a single reg.route(...) call instead of two lines
// that have to be kept in sync. For endpoints that need extra middleware
// (auth, etc.), pass them after method — they run in the order given, same
// as chi's With().
//
// Two calls sharing the same pattern (e.g. GET "/widgets" for list and POST
// "/widgets" for create) are expected and handled automatically — the second
// call's OPTIONS registration replaces the first's with the combined method
// list. Use one *routeRegistry per chi.Router mount (see router.go).
//
// Example, replacing 4 lines of router.go boilerplate with 1:
//
//	reg := newRouteRegistry()
//	reg.route(apiRouter, allowedOrigin, http.MethodGet, "/widgets", handler.List)
//	reg.route(apiRouter, allowedOrigin, http.MethodPost, "/widgets", handler.Create, admin.RequireAdminSession(svc, log))
func (reg *routeRegistry) route(router chi.Router, allowedOrigin, method, pattern string, handler http.HandlerFunc, extraMiddleware ...func(http.Handler) http.Handler) {
	cors := CORS(allowedOrigin, method+", OPTIONS")

	chain := []func(http.Handler) http.Handler{cors}
	chain = append(chain, extraMiddleware...)

	wrapped := http.Handler(handler)
	for i := len(chain) - 1; i >= 0; i-- {
		wrapped = chain[i](wrapped)
	}

	router.Method(method, pattern, wrapped)

	reg.methodsByPattern[pattern] = append(reg.methodsByPattern[pattern], method)
	allMethods := strings.Join(append(append([]string{}, reg.methodsByPattern[pattern]...), "OPTIONS"), ", ")
	preflightCORS := CORS(allowedOrigin, allMethods)
	router.Options(pattern, preflightCORS(noopHandler).ServeHTTP)
}

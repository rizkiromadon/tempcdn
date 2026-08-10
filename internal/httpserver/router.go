package httpserver

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/tempcdn/tempcdn/internal/file"
	"github.com/tempcdn/tempcdn/internal/response"
	"github.com/tempcdn/tempcdn/internal/stats"
	"github.com/tempcdn/tempcdn/internal/upload"
)

type RouterDependencies struct {
	Logger        *slog.Logger
	UploadHandler *upload.Handler
	FileHandler   *file.Handler
	ConfigHandler *upload.ConfigHandler
	StatsHandler  *stats.Handler
	AllowedOrigin string
	// MetricsToken, if non-empty, is required as a Bearer token (or
	// X-Metrics-Token header) on /metrics requests. CORS alone is a
	// browser-enforced policy and does nothing to stop direct
	// curl/server-to-server access, so this is the actual access control.
	MetricsToken   string
	RequestLatency *prometheus.HistogramVec
}

func NewRouter(deps RouterDependencies) http.Handler {
	router := chi.NewRouter()

	router.Use(Recovery(deps.Logger))
	router.Use(RequestLogging(deps.Logger, deps.RequestLatency))

	getCORS := CORS(deps.AllowedOrigin, "GET, OPTIONS")
	healthCORS := CORS(deps.AllowedOrigin, "GET, HEAD, OPTIONS")
	uploadCORS := CORS(deps.AllowedOrigin, "POST, OPTIONS")
	// GET /files/{id} is intentionally permissive (public metadata, safe to
	// read from any origin - see README "CORS" section) while DELETE stays
	// locked to the configured origin, since deletion is a mutating action.
	fileGetCORS := CORS("*", "GET, OPTIONS")
	fileDeleteCORS := CORS(deps.AllowedOrigin, "DELETE, OPTIONS")
	metricsCORS := CORS(deps.AllowedOrigin, "GET, OPTIONS")
	noop := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	router.With(healthCORS).Get("/healthz", handleHealthCheck)
	// HEAD is registered explicitly (chi does not auto-derive it from GET
	// the way net/http.ServeMux does) so uptime/monitoring checks that use
	// HEAD - to avoid pulling a response body on every poll - get a real
	// 200 instead of a 405.
	router.With(healthCORS).Head("/healthz", handleHealthCheckHead)
	router.Options("/healthz", healthCORS(noop).ServeHTTP)
	router.With(metricsCORS, metricsAuth(deps.MetricsToken)).Handle("/metrics", promhttp.Handler())
	router.Options("/metrics", metricsCORS(noop).ServeHTTP)

	router.Route("/api/v1", func(apiRouter chi.Router) {
		apiRouter.With(uploadCORS).Post("/upload", deps.UploadHandler.ServeHTTP)
		apiRouter.Options("/upload", uploadCORS(noop).ServeHTTP)

		apiRouter.With(fileGetCORS).Get("/files/{id}", deps.FileHandler.GetInfo)
		apiRouter.With(fileDeleteCORS).Delete("/files/{id}", deps.FileHandler.Delete)
		// Preflight (OPTIONS) must mirror whichever policy will actually
		// govern the real request, chosen via the browser-sent
		// Access-Control-Request-Method header. Previously this always
		// replied with the GET policy (Allow-Methods: GET, OPTIONS), which
		// does not list DELETE - per the CORS spec, browsers reject the
		// preflight (and therefore the real DELETE call) whenever the
		// requested method isn't in Allow-Methods, breaking browser-based
		// delete entirely regardless of origin. Dispatching by requested
		// method lets GET preflights stay permissive (*) while DELETE
		// preflights stay locked to ALLOWED_ORIGIN, matching the real
		// GET/DELETE responses below.
		apiRouter.Options("/files/{id}", filePreflightCORS(fileGetCORS, fileDeleteCORS, noop).ServeHTTP)

		apiRouter.With(getCORS).Get("/config", deps.ConfigHandler.ServeHTTP)
		apiRouter.Options("/config", getCORS(noop).ServeHTTP)

		// /stats is public, like /config: it's a usage summary (active file
		// counts/bytes, content-type breakdown, lifetime upload totals), not
		// sensitive per-file data, so it uses the same strict-but-open
		// ALLOWED_ORIGIN CORS policy rather than the token-gated /metrics
		// policy.
		apiRouter.With(getCORS).Get("/stats", deps.StatsHandler.ServeHTTP)
		apiRouter.Options("/stats", getCORS(noop).ServeHTTP)
	})

	return router
}

func handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// handleHealthCheckHead answers HEAD /healthz the same way as GET
// (same status code and Content-Type), but per the HTTP spec never writes a
// response body - Go's net/http server already strips any body written by
// the handler for a HEAD request, but writing one anyway here would be
// misleading to read and would do needless work on every monitoring poll.
func handleHealthCheckHead(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
}

// filePreflightCORS dispatches an OPTIONS preflight for /files/{id} to the
// CORS policy matching the method the browser is asking to preflight for
// (via Access-Control-Request-Method), so the preflight response's
// Allow-Origin/Allow-Methods actually matches what the subsequent real
// request will receive. GET (or a missing/unrecognized method - e.g. a
// non-CORS or manually-issued OPTIONS request) falls back to the
// permissive getCORS policy; DELETE gets the strict deleteCORS policy.
func filePreflightCORS(getCORS, deleteCORS func(http.Handler) http.Handler, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Access-Control-Request-Method") == http.MethodDelete {
			deleteCORS(next).ServeHTTP(w, r)
			return
		}
		getCORS(next).ServeHTTP(w, r)
	})
}

// metricsAuth requires a shared-secret token on /metrics, since CORS is a
// browser-enforced policy only and does nothing to stop direct
// curl/server-to-server access. If token is empty, metrics stay open (e.g.
// for local development or when scraping happens over a trusted internal
// network) - operators exposing this port publicly should set one.
func metricsAuth(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if token == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := r.Header.Get("X-Metrics-Token")
			if provided == "" {
				if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
					provided = strings.TrimPrefix(auth, "Bearer ")
				}
			}
			if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
				response.Error(w, http.StatusUnauthorized, "missing or invalid metrics token")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}


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
	"github.com/tempcdn/tempcdn/internal/upload"
)

type RouterDependencies struct {
	Logger        *slog.Logger
	UploadHandler *upload.Handler
	FileHandler   *file.Handler
	ConfigHandler *upload.ConfigHandler
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
	uploadCORS := CORS(deps.AllowedOrigin, "POST, OPTIONS")
	// GET /files/{id} is intentionally permissive (public metadata, safe to
	// read from any origin - see README "CORS" section) while DELETE stays
	// locked to the configured origin, since deletion is a mutating action.
	fileGetCORS := CORS("*", "GET, OPTIONS")
	fileDeleteCORS := CORS(deps.AllowedOrigin, "DELETE, OPTIONS")
	metricsCORS := CORS(deps.AllowedOrigin, "GET, OPTIONS")
	noop := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	router.With(getCORS).Get("/healthz", handleHealthCheck)
	router.Options("/healthz", getCORS(noop).ServeHTTP)
	router.With(metricsCORS, metricsAuth(deps.MetricsToken)).Handle("/metrics", promhttp.Handler())
	router.Options("/metrics", metricsCORS(noop).ServeHTTP)

	router.Route("/api/v1", func(apiRouter chi.Router) {
		apiRouter.With(uploadCORS).Post("/upload", deps.UploadHandler.ServeHTTP)
		apiRouter.Options("/upload", uploadCORS(noop).ServeHTTP)

		apiRouter.With(fileGetCORS).Get("/files/{id}", deps.FileHandler.GetInfo)
		apiRouter.With(fileDeleteCORS).Delete("/files/{id}", deps.FileHandler.Delete)
		// Preflight (OPTIONS) can't know in advance whether the browser's
		// real request will be GET or DELETE, so it responds with the more
		// permissive GET policy here; actual enforcement happens on the
		// real GET/DELETE response via fileGetCORS/fileDeleteCORS above.
		apiRouter.Options("/files/{id}", fileGetCORS(noop).ServeHTTP)

		apiRouter.With(getCORS).Get("/config", deps.ConfigHandler.ServeHTTP)
		apiRouter.Options("/config", getCORS(noop).ServeHTTP)
	})

	return router
}

func handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
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


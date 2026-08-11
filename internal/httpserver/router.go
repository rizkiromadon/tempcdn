package httpserver

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/tempcdn/tempcdn/internal/admin"
	"github.com/tempcdn/tempcdn/internal/file"
	"github.com/tempcdn/tempcdn/internal/nodestatus"
	"github.com/tempcdn/tempcdn/internal/response"
	"github.com/tempcdn/tempcdn/internal/stats"
	"github.com/tempcdn/tempcdn/internal/upload"
)

type RouterDependencies struct {
	Logger            *slog.Logger
	UploadHandler     *upload.Handler
	FileHandler       *file.Handler
	ConfigHandler     *upload.ConfigHandler
	StatsHandler      *stats.Handler
	NodeStatusHandler *nodestatus.Handler
	AdminHandler      *admin.Handler
	AdminService      *admin.Service
	AllowedOrigin     string
	RequestLatency    *prometheus.HistogramVec
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
	router.With(metricsCORS, metricsAuth(deps.AdminService, deps.Logger)).Handle("/metrics", promhttp.Handler())
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

		// /nodes is public read-only liveness info (node IDs, hostnames,
		// online/offline, heartbeat timestamps) - operational visibility,
		// not sensitive per-file data - so it gets the same open
		// ALLOWED_ORIGIN policy as /config and /stats rather than the
		// token-gated /metrics policy.
		apiRouter.With(getCORS).Get("/nodes", deps.NodeStatusHandler.ServeHTTP)
		apiRouter.Options("/nodes", getCORS(noop).ServeHTTP)

		adminCORS := CORS(deps.AllowedOrigin, "GET, POST, OPTIONS")
		apiRouter.Route("/admin", func(adminRouter chi.Router) {
			// /admin/login is intentionally not behind
			// admin.RequireAdminSession - it's how a session is obtained in
			// the first place.
			adminRouter.With(adminCORS).Post("/login", deps.AdminHandler.Login)
			adminRouter.Options("/login", adminCORS(noop).ServeHTTP)

			adminRouter.With(adminCORS, admin.RequireAdminSession(deps.AdminService, deps.Logger)).Post("/logout", deps.AdminHandler.Logout)
			adminRouter.Options("/logout", adminCORS(noop).ServeHTTP)

			adminRouter.With(adminCORS, admin.RequireAdminSession(deps.AdminService, deps.Logger)).Get("/me", deps.AdminHandler.Me)
			adminRouter.Options("/me", adminCORS(noop).ServeHTTP)

			apiKeysCORS := CORS(deps.AllowedOrigin, "GET, POST, DELETE, OPTIONS")
			adminRouter.With(apiKeysCORS, admin.RequireAdminSession(deps.AdminService, deps.Logger)).Post("/api-keys", deps.AdminHandler.CreateAPIKey)
			adminRouter.With(apiKeysCORS, admin.RequireAdminSession(deps.AdminService, deps.Logger)).Get("/api-keys", deps.AdminHandler.ListAPIKeys)
			adminRouter.With(apiKeysCORS, admin.RequireAdminSession(deps.AdminService, deps.Logger)).Delete("/api-keys/{id}", deps.AdminHandler.RevokeAPIKey)
			adminRouter.Options("/api-keys", apiKeysCORS(noop).ServeHTTP)
			adminRouter.Options("/api-keys/{id}", apiKeysCORS(noop).ServeHTTP)

			// /admin/upload-settings lets an authenticated admin read and
			// change the runtime-configurable upload limits (max size,
			// allowed MIME types, blocked extensions) that
			// upload.Validator enforces - see admin.Handler.
			// UpdateUploadSettings for how a change here takes effect
			// immediately on this instance.
			uploadSettingsCORS := CORS(deps.AllowedOrigin, "GET, PUT, OPTIONS")
			adminRouter.With(uploadSettingsCORS, admin.RequireAdminSession(deps.AdminService, deps.Logger)).Get("/upload-settings", deps.AdminHandler.GetUploadSettings)
			adminRouter.With(uploadSettingsCORS, admin.RequireAdminSession(deps.AdminService, deps.Logger)).Put("/upload-settings", deps.AdminHandler.UpdateUploadSettings)
			adminRouter.Options("/upload-settings", uploadSettingsCORS(noop).ServeHTTP)
		})
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

// metricsAuth requires either a valid admin session or a valid,
// non-revoked API key (created via POST /api/v1/admin/api-keys) on
// /metrics, since CORS is a browser-enforced policy only and does nothing
// to stop direct curl/server-to-server access. API keys are the
// replacement for the old static METRICS_TOKEN environment variable:
// database-backed and revocable from the admin dashboard, rather than a
// single shared secret that could only be rotated by redeploying with a
// new environment variable. Metrics access is gated unconditionally
// whenever an admin service is configured - unlike the old METRICS_TOKEN
// default-open behavior, there is no "unset" state here, since an admin
// account (and therefore the ability to mint an API key) always exists
// once the server has bootstrapped.
func metricsAuth(adminService *admin.Service, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if adminService == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			credential := extractBearerOrHeaderToken(r)
			if admin.VerifyAPIKeyOrAdminSession(r.Context(), credential, adminService, logger) {
				next.ServeHTTP(w, r)
				return
			}
			response.Error(w, http.StatusUnauthorized, "missing or invalid metrics credentials")
		})
	}
}

// extractBearerOrHeaderToken reads a credential from the X-Metrics-Token
// header (kept as an alternate header name for existing Prometheus scrape
// configs), falling back to a Bearer Authorization header. The same
// plaintext value is tried against both an admin session and an API key
// (see admin.VerifyAPIKeyOrAdminSession), since a caller supplies one or
// the other, not both.
func extractBearerOrHeaderToken(r *http.Request) string {
	if provided := r.Header.Get("X-Metrics-Token"); provided != "" {
		return provided
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return ""
}

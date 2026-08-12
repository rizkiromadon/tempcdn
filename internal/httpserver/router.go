package httpserver

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rizkiromadon/tempcdn/internal/admin"
	"github.com/rizkiromadon/tempcdn/internal/file"
	"github.com/rizkiromadon/tempcdn/internal/legal"
	"github.com/rizkiromadon/tempcdn/internal/nodestatus"
	"github.com/rizkiromadon/tempcdn/internal/response"
	"github.com/rizkiromadon/tempcdn/internal/stats"
	"github.com/rizkiromadon/tempcdn/internal/upload"
)

type RouterDependencies struct {
	Logger            *slog.Logger
	UploadHandler     *upload.Handler
	FileHandler       *file.Handler
	ConfigHandler     *upload.ConfigHandler
	StatsHandler      *stats.Handler
	NodeStatusHandler *nodestatus.Handler
	LegalHandler      *legal.Handler
	AdminHandler      *admin.Handler
	AdminService      *admin.Service
	AllowedOrigin     string
	RequestLatency    *prometheus.HistogramVec
}

func NewRouter(deps RouterDependencies) http.Handler {
	router := chi.NewRouter()

	router.Use(Recovery(deps.Logger))
	router.Use(RequestLogging(deps.Logger, deps.RequestLatency))

	origin := deps.AllowedOrigin

	healthCORS := CORS(origin, "GET, HEAD, OPTIONS")
	router.With(healthCORS).Get("/healthz", handleHealthCheck)
	router.With(healthCORS).Head("/healthz", handleHealthCheckHead)
	router.Options("/healthz", healthCORS(noopHandler).ServeHTTP)

	metricsCORS := CORS(origin, "GET, OPTIONS")
	router.With(metricsCORS, metricsAuth(deps.AdminService, deps.Logger)).Handle("/metrics", promhttp.Handler())
	router.Options("/metrics", metricsCORS(noopHandler).ServeHTTP)

	router.Route("/api/v1", func(apiRouter chi.Router) {
		apiReg := newRouteRegistry()
		apiReg.route(apiRouter, origin, http.MethodPost, "/upload", deps.UploadHandler.ServeHTTP)

		// /files/{id} is the one endpoint with two different CORS policies
		// on the same path (public GET, restricted-origin DELETE), so it's
		// wired by hand instead of through route(). Every other endpoint
		// below is a single-method, single-CORS-policy resource and uses
		// route() — see middleware.go for what that one call expands to.
		fileGetCORS := CORS("*", "GET, OPTIONS")
		fileDeleteCORS := CORS(origin, "DELETE, OPTIONS")
		apiRouter.With(fileGetCORS).Get("/files/{id}", deps.FileHandler.GetInfo)
		apiRouter.With(fileDeleteCORS).Delete("/files/{id}", deps.FileHandler.Delete)
		apiRouter.Options("/files/{id}", filePreflightCORS(fileGetCORS, fileDeleteCORS, noopHandler).ServeHTTP)

		apiReg.route(apiRouter, origin, http.MethodGet, "/config", deps.ConfigHandler.ServeHTTP)
		apiReg.route(apiRouter, origin, http.MethodGet, "/stats", deps.StatsHandler.ServeHTTP)
		apiReg.route(apiRouter, origin, http.MethodGet, "/nodes", deps.NodeStatusHandler.ServeHTTP)

		apiReg.route(apiRouter, origin, http.MethodGet, "/legal/terms", deps.LegalHandler.Terms)
		apiReg.route(apiRouter, origin, http.MethodGet, "/legal/privacy", deps.LegalHandler.Privacy)

		requireAdmin := admin.RequireAdminSession(deps.AdminService, deps.Logger)

		apiRouter.Route("/admin", func(adminRouter chi.Router) {
			adminReg := newRouteRegistry()
			adminReg.route(adminRouter, origin, http.MethodPost, "/login", deps.AdminHandler.Login)
			adminReg.route(adminRouter, origin, http.MethodPost, "/logout", deps.AdminHandler.Logout, requireAdmin)
			adminReg.route(adminRouter, origin, http.MethodGet, "/me", deps.AdminHandler.Me, requireAdmin)

			adminReg.route(adminRouter, origin, http.MethodPost, "/api-keys", deps.AdminHandler.CreateAPIKey, requireAdmin)
			adminReg.route(adminRouter, origin, http.MethodGet, "/api-keys", deps.AdminHandler.ListAPIKeys, requireAdmin)
			adminReg.route(adminRouter, origin, http.MethodDelete, "/api-keys/{id}", deps.AdminHandler.RevokeAPIKey, requireAdmin)

			adminReg.route(adminRouter, origin, http.MethodGet, "/upload-settings", deps.AdminHandler.GetUploadSettings, requireAdmin)
			adminReg.route(adminRouter, origin, http.MethodPut, "/upload-settings", deps.AdminHandler.UpdateUploadSettings, requireAdmin)

			adminReg.route(adminRouter, origin, http.MethodGet, "/legal/terms", deps.AdminHandler.GetTerms, requireAdmin)
			adminReg.route(adminRouter, origin, http.MethodPut, "/legal/terms", deps.AdminHandler.UpdateTerms, requireAdmin)
			adminReg.route(adminRouter, origin, http.MethodGet, "/legal/privacy", deps.AdminHandler.GetPrivacy, requireAdmin)
			adminReg.route(adminRouter, origin, http.MethodPut, "/legal/privacy", deps.AdminHandler.UpdatePrivacy, requireAdmin)
		})
	})

	return router
}

func handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func handleHealthCheckHead(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
}

func filePreflightCORS(getCORS, deleteCORS func(http.Handler) http.Handler, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Access-Control-Request-Method") == http.MethodDelete {
			deleteCORS(next).ServeHTTP(w, r)
			return
		}
		getCORS(next).ServeHTTP(w, r)
	})
}

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

func extractBearerOrHeaderToken(r *http.Request) string {
	if provided := r.Header.Get("X-Metrics-Token"); provided != "" {
		return provided
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return ""
}

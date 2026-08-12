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

	fileGetCORS := CORS("*", "GET, OPTIONS")
	fileDeleteCORS := CORS(deps.AllowedOrigin, "DELETE, OPTIONS")
	metricsCORS := CORS(deps.AllowedOrigin, "GET, OPTIONS")
	noop := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	router.With(healthCORS).Get("/healthz", handleHealthCheck)

	router.With(healthCORS).Head("/healthz", handleHealthCheckHead)
	router.Options("/healthz", healthCORS(noop).ServeHTTP)
	router.With(metricsCORS, metricsAuth(deps.AdminService, deps.Logger)).Handle("/metrics", promhttp.Handler())
	router.Options("/metrics", metricsCORS(noop).ServeHTTP)

	router.Route("/api/v1", func(apiRouter chi.Router) {
		apiRouter.With(uploadCORS).Post("/upload", deps.UploadHandler.ServeHTTP)
		apiRouter.Options("/upload", uploadCORS(noop).ServeHTTP)

		apiRouter.With(fileGetCORS).Get("/files/{id}", deps.FileHandler.GetInfo)
		apiRouter.With(fileDeleteCORS).Delete("/files/{id}", deps.FileHandler.Delete)

		apiRouter.Options("/files/{id}", filePreflightCORS(fileGetCORS, fileDeleteCORS, noop).ServeHTTP)

		apiRouter.With(getCORS).Get("/config", deps.ConfigHandler.ServeHTTP)
		apiRouter.Options("/config", getCORS(noop).ServeHTTP)

		apiRouter.With(getCORS).Get("/stats", deps.StatsHandler.ServeHTTP)
		apiRouter.Options("/stats", getCORS(noop).ServeHTTP)

		apiRouter.With(getCORS).Get("/nodes", deps.NodeStatusHandler.ServeHTTP)
		apiRouter.Options("/nodes", getCORS(noop).ServeHTTP)

		adminCORS := CORS(deps.AllowedOrigin, "GET, POST, OPTIONS")
		apiRouter.Route("/admin", func(adminRouter chi.Router) {

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

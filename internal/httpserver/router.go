package httpserver

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/tempcdn/tempcdn/internal/file"
	"github.com/tempcdn/tempcdn/internal/upload"
)

type RouterDependencies struct {
	Logger        *slog.Logger
	UploadHandler *upload.Handler
	FileHandler   *file.Handler
	AllowedOrigin string
}

func NewRouter(deps RouterDependencies) http.Handler {
	router := chi.NewRouter()

	router.Use(Recovery(deps.Logger))
	router.Use(RequestLogging(deps.Logger))

	getCORS := CORS(deps.AllowedOrigin, "GET, OPTIONS")
	uploadCORS := CORS(deps.AllowedOrigin, "POST, OPTIONS")
	fileCORS := CORS(deps.AllowedOrigin, "GET, DELETE, OPTIONS")
	metricsCORS := CORS(deps.AllowedOrigin, "GET, OPTIONS")
	noop := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	router.With(getCORS).Get("/healthz", handleHealthCheck)
	router.Options("/healthz", getCORS(noop).ServeHTTP)
	router.With(metricsCORS).Handle("/metrics", promhttp.Handler())
	router.Options("/metrics", metricsCORS(noop).ServeHTTP)

	router.Route("/api/v1", func(apiRouter chi.Router) {
		apiRouter.With(uploadCORS).Post("/upload", deps.UploadHandler.ServeHTTP)
		apiRouter.Options("/upload", uploadCORS(noop).ServeHTTP)

		apiRouter.With(fileCORS).Get("/files/{id}", deps.FileHandler.GetInfo)
		apiRouter.With(fileCORS).Delete("/files/{id}", deps.FileHandler.Delete)
		apiRouter.Options("/files/{id}", fileCORS(noop).ServeHTTP)
	})

	return router
}

func handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}


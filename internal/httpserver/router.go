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
}

func NewRouter(deps RouterDependencies) http.Handler {
	router := chi.NewRouter()

	router.Use(Recovery(deps.Logger))
	router.Use(RequestLogging(deps.Logger))

	router.Get("/healthz", handleHealthCheck)
	router.Handle("/metrics", promhttp.Handler())

	router.Route("/api/v1", func(apiRouter chi.Router) {
		apiRouter.With(StrictCORS("")).Post("/upload", deps.UploadHandler.ServeHTTP)

		apiRouter.With(PermissiveCORS).Get("/files/{id}", deps.FileHandler.GetInfo)
		apiRouter.With(StrictCORS("")).Delete("/files/{id}", deps.FileHandler.Delete)
	})

	return router
}

func handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

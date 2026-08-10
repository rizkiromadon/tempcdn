package httpserver

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tempcdn/tempcdn/internal/response"
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
					// If the handler already wrote a status/body before
					// panicking, headers are already sent and we can't
					// change them - writing again would just log a
					// harmless "superfluous WriteHeader" warning and have
					// no effect. Only emit the documented error body when
					// nothing has been written yet.
					if !recorder.wroteHeader {
						response.Error(recorder, http.StatusInternalServerError, "internal server error")
					}
				}
			}()
			next.ServeHTTP(recorder, r)
		})
	}
}

// CORS returns a middleware that sets CORS headers for the given allowedOrigin
// and the given list of allowed methods (OPTIONS is always included for preflight).
// allowedOrigin must be non-empty; callers should validate configuration at
// startup (see config.Config.validate) rather than silently omitting the header.
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

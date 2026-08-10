// Package stats exposes a public, human/dashboard-friendly summary of CDN
// usage at GET /api/v1/stats. It is intentionally separate from GET /metrics:
// /metrics is the Prometheus scrape target (text exposition format, may be
// token-gated per METRICS_TOKEN) while this endpoint returns plain JSON,
// stays open like /config, and mixes in current-state figures from the
// metadata store that Prometheus counters alone can't provide (e.g. how many
// files are active right now).
package stats

import (
	"fmt"
	"net/http"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tempcdn/tempcdn/internal/metadata"
	"github.com/tempcdn/tempcdn/internal/response"
)

// Response is the JSON body returned by GET /api/v1/stats.
type Response struct {
	// Active* reflect the files table right now: rows the sweeper and
	// DELETE /files/{id} haven't removed yet. They fall as files expire or
	// are deleted, unlike the Lifetime* counters below.
	ActiveFileCount int64 `json:"active_file_count"`
	ActiveBytes     int64 `json:"active_bytes"`
	// AverageFileBytes is 0 when there are no active files, rather than NaN
	// or a divide-by-zero, so the field is always a well-formed number for
	// JSON consumers.
	AverageFileBytes int64 `json:"average_file_bytes"`
	// ContentTypeBreakdown maps top-level MIME type (e.g. "image", "video")
	// to the count of currently-active files of that type.
	ContentTypeBreakdown map[string]int64 `json:"content_type_breakdown"`

	// Lifetime* figures are sourced from Prometheus counters recorded at
	// upload time (see httpserver.Metrics). They only ever increase - they
	// are NOT reduced when files expire or are deleted - so they represent
	// all-time totals since the process last restarted (Prometheus counters
	// reset to 0 on restart; they are not persisted across deploys).
	LifetimeUploadsTotal      int64 `json:"lifetime_uploads_total"`
	LifetimeUploadBytesTotal  int64 `json:"lifetime_upload_bytes_total"`
	LifetimeUploadErrorsTotal int64 `json:"lifetime_upload_errors_total"`

	GeneratedAt string `json:"generated_at"`
}

// Handler serves GET /api/v1/stats.
type Handler struct {
	repository        metadata.Repository
	uploadsTotal      prometheus.Counter
	uploadBytesTotal  prometheus.Counter
	uploadErrorsTotal prometheus.Counter
	now               func() time.Time
}

func NewHandler(repository metadata.Repository, uploadsTotal, uploadBytesTotal, uploadErrorsTotal prometheus.Counter) *Handler {
	return &Handler{
		repository:        repository,
		uploadsTotal:      uploadsTotal,
		uploadBytesTotal:  uploadBytesTotal,
		uploadErrorsTotal: uploadErrorsTotal,
		now:               time.Now,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	activeStats, err := h.repository.Stats(r.Context(), h.now().UTC())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "failed to compute stats")
		return
	}

	uploadsTotal, err := readCounterValue(h.uploadsTotal)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "failed to read upload metrics")
		return
	}
	uploadBytesTotal, err := readCounterValue(h.uploadBytesTotal)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "failed to read upload metrics")
		return
	}
	uploadErrorsTotal, err := readCounterValue(h.uploadErrorsTotal)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "failed to read upload metrics")
		return
	}

	var averageBytes int64
	if activeStats.ActiveFileCount > 0 {
		averageBytes = activeStats.ActiveBytes / activeStats.ActiveFileCount
	}

	// Normalize a nil breakdown to an empty (but non-null) map so the JSON
	// field is always "{}" rather than "null" when there are no active
	// files, which is friendlier for consumers that index into it directly.
	breakdown := activeStats.ContentTypeBreakdown
	if breakdown == nil {
		breakdown = map[string]int64{}
	}

	body := Response{
		ActiveFileCount:           activeStats.ActiveFileCount,
		ActiveBytes:               activeStats.ActiveBytes,
		AverageFileBytes:          averageBytes,
		ContentTypeBreakdown:      breakdown,
		LifetimeUploadsTotal:      uploadsTotal,
		LifetimeUploadBytesTotal:  uploadBytesTotal,
		LifetimeUploadErrorsTotal: uploadErrorsTotal,
		GeneratedAt:               h.now().UTC().Format("2006-01-02T15:04:05Z07:00"),
	}

	response.JSON(w, http.StatusOK, body)
}

// readCounterValue extracts the current value of a Prometheus counter
// in-process, without going through an HTTP scrape. Counter satisfies
// prometheus.Metric, whose Write method is the library's own supported way
// to read a metric's current value programmatically.
func readCounterValue(counter prometheus.Counter) (int64, error) {
	var metric dto.Metric
	if err := counter.Write(&metric); err != nil {
		return 0, fmt.Errorf("write counter metric: %w", err)
	}
	if metric.Counter == nil {
		return 0, fmt.Errorf("counter metric missing Counter field")
	}
	return int64(metric.Counter.GetValue()), nil
}

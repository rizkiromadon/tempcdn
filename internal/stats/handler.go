
package stats

import (
	"fmt"
	"net/http"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rizkiromadon/tempcdn/internal/metadata"
	"github.com/rizkiromadon/tempcdn/internal/response"
)

type Response struct {

	ActiveFileCount int64 `json:"active_file_count"`
	ActiveBytes     int64 `json:"active_bytes"`

	AverageFileBytes int64 `json:"average_file_bytes"`

	ContentTypeBreakdown map[string]int64 `json:"content_type_breakdown"`

	LifetimeUploadsTotal      int64 `json:"lifetime_uploads_total"`
	LifetimeUploadBytesTotal  int64 `json:"lifetime_upload_bytes_total"`
	LifetimeUploadErrorsTotal int64 `json:"lifetime_upload_errors_total"`

	GeneratedAt string `json:"generated_at"`
}

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

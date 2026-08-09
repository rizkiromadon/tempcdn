package httpserver

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type Metrics struct {
	UploadsTotal      prometheus.Counter
	UploadBytesTotal  prometheus.Counter
	UploadErrorsTotal prometheus.Counter
	RequestLatency    *prometheus.HistogramVec
}

func NewMetrics() *Metrics {
	return &Metrics{
		UploadsTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "tempcdn_uploads_total",
			Help: "Total number of successful uploads processed",
		}),
		UploadBytesTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "tempcdn_upload_bytes_total",
			Help: "Total number of bytes uploaded",
		}),
		UploadErrorsTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "tempcdn_upload_errors_total",
			Help: "Total number of failed upload attempts",
		}),
		RequestLatency: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "tempcdn_request_latency_seconds",
			Help:    "Request latency in seconds",
			Buckets: prometheus.DefBuckets,
		}, []string{"path", "method"}),
	}
}

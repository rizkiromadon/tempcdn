package upload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tempcdn/tempcdn/internal/metadata"
	"github.com/tempcdn/tempcdn/internal/ratelimit"
	"github.com/tempcdn/tempcdn/internal/response"
)

type Handler struct {
	service *Service
	// validator is consulted for the current max upload size on every
	// request (via Validator.Snapshot), rather than a size baked in at
	// NewHandler time, so that an admin raising or lowering the limit
	// through PUT /api/v1/admin/upload-settings takes effect on the very
	// next upload - including the MaxBytesReader cap below, which has to
	// be enforced before the body is even read, not just in
	// Service.Upload's later ValidateSize call.
	validator          *Validator
	concurrencyLimiter *ratelimit.ConcurrencyLimiter
	ipHashSalt         string
	uploadsTotal       prometheus.Counter
	uploadBytesTotal   prometheus.Counter
	uploadErrorsTotal  prometheus.Counter
	logger             *slog.Logger
}

func NewHandler(service *Service, validator *Validator, concurrencyLimiter *ratelimit.ConcurrencyLimiter, ipHashSalt string, uploadsTotal prometheus.Counter, uploadBytesTotal prometheus.Counter, uploadErrorsTotal prometheus.Counter, logger *slog.Logger) *Handler {
	return &Handler{
		service:            service,
		validator:          validator,
		concurrencyLimiter: concurrencyLimiter,
		ipHashSalt:         ipHashSalt,
		uploadsTotal:       uploadsTotal,
		uploadBytesTotal:   uploadBytesTotal,
		uploadErrorsTotal:  uploadErrorsTotal,
		logger:             logger,
	}
}

type uploadResponseBody struct {
	ID             string `json:"id"`
	OriginalName   string `json:"original_name"`
	ContentType    string `json:"content_type"`
	SizeBytes      int64  `json:"size_bytes"`
	ChecksumSHA256 string `json:"checksum_sha256"`
	ObjectKey      string `json:"object_key"`
	CDNURL         string `json:"cdn_url"`
	CreatedAt      string `json:"created_at"`
	ExpiresAt      string `json:"expires_at"`
	Duplicate      bool   `json:"duplicate"`
	// DeleteToken authorizes DELETE /api/v1/files/{id}. It is shown only
	// once, in this response, and is empty for duplicate uploads (the
	// original uploader already holds the real one).
	DeleteToken string `json:"delete_token,omitempty"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	clientIP := extractClientIP(r)
	ipHash := hashIP(clientIP, h.ipHashSalt)

	if !h.concurrencyLimiter.TryAcquire() {
		response.Error(w, http.StatusServiceUnavailable, "server is busy, please try again shortly")
		return
	}
	defer h.concurrencyLimiter.Release()

	maxUploadSizeBytes, _, _ := h.validator.Snapshot()
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSizeBytes+(1<<20))

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid multipart form data")
		return
	}

	file, fileHeader, err := r.FormFile("file")
	if err != nil {
		response.Error(w, http.StatusBadRequest, "missing file field in form data")
		return
	}
	defer file.Close()

	result, err := h.service.Upload(r.Context(), Input{
		OriginalName:   fileHeader.Filename,
		SizeBytes:      fileHeader.Size,
		Content:        file,
		UploaderIPHash: ipHash,
	})
	if err != nil {
		h.uploadErrorsTotal.Inc()
		writeUploadError(h.logger, w, err)
		return
	}

	h.uploadsTotal.Inc()
	h.uploadBytesTotal.Add(float64(result.Record.SizeBytes))

	response.JSON(w, http.StatusOK, toUploadResponseBody(result))
}

func writeUploadError(logger *slog.Logger, w http.ResponseWriter, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		response.Error(w, http.StatusGatewayTimeout, "upload timed out")
		return
	}

	// metadata.ErrFileNotFound is the *expected*, non-error outcome of the
	// dedup lookup inside Service.Upload (it means "not a duplicate,
	// proceed") and is handled internally there - it is never itself
	// returned as Upload's error. This check is defensive only, guarding
	// against a future refactor accidentally letting it leak through; if it
	// ever fires, that's a bug worth knowing about, so it's logged as one.
	if errors.Is(err, metadata.ErrFileNotFound) {
		if logger != nil {
			logger.Error("unexpected_err_file_not_found_in_upload_path", "error", err)
		}
		response.Error(w, http.StatusInternalServerError, "internal error processing upload")
		return
	}

	var validationErr *ValidationError
	if errors.As(err, &validationErr) {
		response.Error(w, http.StatusBadRequest, validationErr.Error())
		return
	}

	// Anything else (temp file creation, storage puts, DB writes, the
	// spooled-vs-declared size mismatch) is an internal/infra failure, not
	// the client's fault - don't leak raw Go error text (temp paths, driver
	// messages) and don't mislabel it as a 400.
	if logger != nil {
		logger.Error("upload_failed", "error", err)
	}
	response.Error(w, http.StatusInternalServerError, "failed to process upload")
}

func toUploadResponseBody(result *Result) uploadResponseBody {
	record := result.Record
	return uploadResponseBody{
		ID:             record.ID,
		OriginalName:   record.OriginalName,
		ContentType:    record.ContentType,
		SizeBytes:      record.SizeBytes,
		ChecksumSHA256: record.ChecksumSHA256,
		ObjectKey:      record.ObjectKey,
		CDNURL:         record.CDNURL,
		CreatedAt:      record.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		ExpiresAt:      record.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"),
		Duplicate:      result.Duplicate,
		DeleteToken:    result.DeleteToken,
	}
}

func extractClientIP(r *http.Request) string {
	if cfConnectingIP := r.Header.Get("CF-Connecting-IP"); cfConnectingIP != "" {
		return cfConnectingIP
	}
	if forwardedFor := r.Header.Get("X-Forwarded-For"); forwardedFor != "" {
		firstHop := strings.TrimSpace(strings.Split(forwardedFor, ",")[0])
		if firstHop != "" {
			return firstHop
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func hashIP(ip string, salt string) string {
	hasher := sha256.New()
	hasher.Write([]byte(salt))
	hasher.Write([]byte(ip))
	return hex.EncodeToString(hasher.Sum(nil))
}

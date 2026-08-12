package file

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rizkiromadon/tempcdn/internal/response"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

type infoResponseBody struct {
	ID             string `json:"id"`
	OriginalName   string `json:"original_name"`
	ContentType    string `json:"content_type"`
	SizeBytes      int64  `json:"size_bytes"`
	ChecksumSHA256 string `json:"checksum_sha256"`
	ObjectKey      string `json:"object_key"`
	CDNURL         string `json:"cdn_url"`
	CreatedAt      string `json:"created_at"`
	ExpiresAt      string `json:"expires_at"`
	Expired        bool   `json:"expired"`
}

func (h *Handler) GetInfo(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	info, err := h.service.GetInfo(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		response.Error(w, http.StatusNotFound, "file not found")
		return
	}
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "failed to retrieve file info")
		return
	}

	record := info.Record
	body := infoResponseBody{
		ID:             record.ID,
		OriginalName:   record.OriginalName,
		ContentType:    record.ContentType,
		SizeBytes:      record.SizeBytes,
		ChecksumSHA256: record.ChecksumSHA256,
		ObjectKey:      record.ObjectKey,
		CDNURL:         record.CDNURL,
		CreatedAt:      record.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		ExpiresAt:      record.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"),
		Expired:        info.Expired,
	}

	if info.Expired {
		response.JSON(w, http.StatusGone, body)
		return
	}
	response.JSON(w, http.StatusOK, body)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	deleteToken := extractDeleteToken(r)

	err := h.service.DeleteBeforeTTL(r.Context(), id, deleteToken)
	if errors.Is(err, ErrNotFound) {
		response.Error(w, http.StatusNotFound, "file not found")
		return
	}
	if errors.Is(err, ErrInvalidDeleteToken) {
		response.Error(w, http.StatusForbidden, "invalid or missing delete token")
		return
	}
	if errors.Is(err, ErrAlreadyExpired) {
		response.Error(w, http.StatusGone, "file has already expired")
		return
	}
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "failed to delete file")
		return
	}

	response.JSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func extractDeleteToken(r *http.Request) string {
	if header := r.Header.Get("X-Delete-Token"); header != "" {
		return header
	}
	return r.URL.Query().Get("delete_token")
}

// Package legal serves the public, read-only side of admin-editable legal
// documents (terms of service, privacy policy). The admin-facing
// read/write endpoints for the same documents live in internal/admin,
// alongside the other admin-editable settings (e.g. upload settings) —
// this package only exposes the public GET.
package legal

import (
	"errors"
	"net/http"

	"github.com/rizkiromadon/tempcdn/internal/metadata"
	"github.com/rizkiromadon/tempcdn/internal/response"
)

type responseBody struct {
	DocType   string  `json:"doc_type"`
	Content   string  `json:"content"`
	UpdatedAt string  `json:"updated_at"`
	UpdatedBy *string `json:"updated_by,omitempty"`
}

const apiTimeFormat = "2006-01-02T15:04:05Z07:00"

type Handler struct {
	repository metadata.LegalDocumentRepository
}

func NewHandler(repository metadata.LegalDocumentRepository) *Handler {
	return &Handler{repository: repository}
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request, docType string) {
	doc, err := h.repository.GetLegalDocument(r.Context(), docType)
	if errors.Is(err, metadata.ErrLegalDocumentNotFound) {
		response.Error(w, http.StatusNotFound, "legal document not found")
		return
	}
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "failed to get legal document")
		return
	}

	response.JSON(w, http.StatusOK, responseBody{
		DocType:   doc.DocType,
		Content:   doc.Content,
		UpdatedAt: doc.UpdatedAt.Format(apiTimeFormat),
		UpdatedBy: doc.UpdatedBy,
	})
}

func (h *Handler) Terms(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, metadata.LegalDocTerms)
}

func (h *Handler) Privacy(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, metadata.LegalDocPrivacy)
}

package admin

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rizkiromadon/tempcdn/internal/metadata"
	"github.com/rizkiromadon/tempcdn/internal/response"
)

type Handler struct {
	service *Service
	logger  *slog.Logger

	onUploadSettingsUpdated func(*metadata.UploadSettings)
}

func NewHandler(service *Service, logger *slog.Logger) *Handler {
	return &Handler{service: service, logger: logger}
}

func (h *Handler) SetUploadSettingsUpdatedCallback(fn func(*metadata.UploadSettings)) {
	h.onUploadSettingsUpdated = fn
}

type loginRequestBody struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponseBody struct {
	Token     string `json:"token"`
	Username  string `json:"username"`
	ExpiresAt string `json:"expires_at"`
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var body loginRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	result, err := h.service.Login(r.Context(), body.Username, body.Password)
	if errors.Is(err, ErrInvalidCredentials) {
		response.Error(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if err != nil {
		h.logger.Error("admin_login_failed", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to log in")
		return
	}

	response.JSON(w, http.StatusOK, loginResponseBody{
		Token:     result.Token,
		Username:  result.Admin.Username,
		ExpiresAt: result.Session.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"),
	})
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	token := extractBearerToken(r)
	if err := h.service.Logout(r.Context(), token); err != nil {
		h.logger.Error("admin_logout_failed", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to log out")
		return
	}
	response.JSON(w, http.StatusOK, map[string]bool{"logged_out": true})
}

func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	session, ok := SessionFromContext(r.Context())
	if !ok {

		response.Error(w, http.StatusInternalServerError, "missing session context")
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{
		"username": session.Admin.Username,
	})
}

type createAPIKeyRequestBody struct {
	Name string `json:"name"`
}

type createAPIKeyResponseBody struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Key       string `json:"key"`
	CreatedAt string `json:"created_at"`
}

type apiKeyResponseBody struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at,omitempty"`
	RevokedAt  *string `json:"revoked_at,omitempty"`
}

const apiTimeFormat = "2006-01-02T15:04:05Z07:00"

func (h *Handler) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var body createAPIKeyRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		response.Error(w, http.StatusBadRequest, "name must not be empty")
		return
	}

	result, err := h.service.CreateAPIKey(r.Context(), body.Name)
	if err != nil {
		h.logger.Error("admin_create_api_key_failed", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to create api key")
		return
	}

	response.JSON(w, http.StatusCreated, createAPIKeyResponseBody{
		ID:        result.Record.ID,
		Name:      result.Record.Name,
		Key:       result.Key,
		CreatedAt: result.Record.CreatedAt.Format(apiTimeFormat),
	})
}

func (h *Handler) ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.service.ListAPIKeys(r.Context())
	if err != nil {
		h.logger.Error("admin_list_api_keys_failed", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to list api keys")
		return
	}

	body := make([]apiKeyResponseBody, 0, len(keys))
	for _, key := range keys {
		body = append(body, toAPIKeyResponseBody(key))
	}
	response.JSON(w, http.StatusOK, body)
}

func (h *Handler) RevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.service.RevokeAPIKey(r.Context(), id); err != nil {
		h.logger.Error("admin_revoke_api_key_failed", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to revoke api key")
		return
	}
	response.JSON(w, http.StatusOK, map[string]bool{"revoked": true})
}

func toAPIKeyResponseBody(key *metadata.APIKey) apiKeyResponseBody {
	body := apiKeyResponseBody{
		ID:        key.ID,
		Name:      key.Name,
		CreatedAt: key.CreatedAt.Format(apiTimeFormat),
	}
	if key.LastUsedAt != nil {
		formatted := key.LastUsedAt.Format(apiTimeFormat)
		body.LastUsedAt = &formatted
	}
	if key.RevokedAt != nil {
		formatted := key.RevokedAt.Format(apiTimeFormat)
		body.RevokedAt = &formatted
	}
	return body
}

type uploadSettingsResponseBody struct {
	MaxUploadSizeMB   int64    `json:"max_upload_size_mb"`
	AllowedMimeTypes  []string `json:"allowed_mime_types"`
	BlockedExtensions []string `json:"blocked_extensions"`
	UpdatedAt         string   `json:"updated_at"`
	UpdatedBy         *string  `json:"updated_by,omitempty"`
}

func toUploadSettingsResponseBody(settings *metadata.UploadSettings) uploadSettingsResponseBody {
	return uploadSettingsResponseBody{
		MaxUploadSizeMB:   settings.MaxUploadSizeMB,
		AllowedMimeTypes:  settings.AllowedMimeTypes,
		BlockedExtensions: settings.BlockedExtensions,
		UpdatedAt:         settings.UpdatedAt.Format(apiTimeFormat),
		UpdatedBy:         settings.UpdatedBy,
	}
}

func (h *Handler) GetUploadSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := h.service.GetUploadSettings(r.Context())
	if err != nil {
		h.logger.Error("admin_get_upload_settings_failed", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to get upload settings")
		return
	}
	response.JSON(w, http.StatusOK, toUploadSettingsResponseBody(settings))
}

type updateUploadSettingsRequestBody struct {
	MaxUploadSizeMB   int64    `json:"max_upload_size_mb"`
	AllowedMimeTypes  []string `json:"allowed_mime_types"`
	BlockedExtensions []string `json:"blocked_extensions"`
}

func (h *Handler) UpdateUploadSettings(w http.ResponseWriter, r *http.Request) {
	var body updateUploadSettingsRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	session, ok := SessionFromContext(r.Context())
	if !ok {

		response.Error(w, http.StatusInternalServerError, "missing session context")
		return
	}

	settings, err := h.service.UpdateUploadSettings(r.Context(), session.Admin.ID, UpdateUploadSettingsInput{
		MaxUploadSizeMB:   body.MaxUploadSizeMB,
		AllowedMimeTypes:  body.AllowedMimeTypes,
		BlockedExtensions: body.BlockedExtensions,
	})
	if errors.Is(err, ErrInvalidUploadSettings) {
		response.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		h.logger.Error("admin_update_upload_settings_failed", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to update upload settings")
		return
	}

	if h.onUploadSettingsUpdated != nil {
		h.onUploadSettingsUpdated(settings)
	}

	response.JSON(w, http.StatusOK, toUploadSettingsResponseBody(settings))
}

type legalDocumentResponseBody struct {
	DocType   string  `json:"doc_type"`
	Content   string  `json:"content"`
	UpdatedAt string  `json:"updated_at"`
	UpdatedBy *string `json:"updated_by,omitempty"`
}

func toLegalDocumentResponseBody(doc *metadata.LegalDocument) legalDocumentResponseBody {
	return legalDocumentResponseBody{
		DocType:   doc.DocType,
		Content:   doc.Content,
		UpdatedAt: doc.UpdatedAt.Format(apiTimeFormat),
		UpdatedBy: doc.UpdatedBy,
	}
}

func (h *Handler) getLegalDocument(w http.ResponseWriter, r *http.Request, docType string) {
	doc, err := h.service.GetLegalDocument(r.Context(), docType)
	if errors.Is(err, metadata.ErrLegalDocumentNotFound) {
		response.Error(w, http.StatusNotFound, "legal document not found")
		return
	}
	if err != nil {
		h.logger.Error("admin_get_legal_document_failed", "error", err, "doc_type", docType)
		response.Error(w, http.StatusInternalServerError, "failed to get legal document")
		return
	}
	response.JSON(w, http.StatusOK, toLegalDocumentResponseBody(doc))
}

type updateLegalDocumentRequestBody struct {
	Content string `json:"content"`
}

func (h *Handler) updateLegalDocument(w http.ResponseWriter, r *http.Request, docType string) {
	var body updateLegalDocumentRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	session, ok := SessionFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusInternalServerError, "missing session context")
		return
	}

	doc, err := h.service.UpdateLegalDocument(r.Context(), docType, body.Content, session.Admin.ID)
	if errors.Is(err, ErrLegalDocumentContentEmpty) {
		response.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if errors.Is(err, metadata.ErrLegalDocumentNotFound) {
		response.Error(w, http.StatusNotFound, "legal document not found")
		return
	}
	if err != nil {
		h.logger.Error("admin_update_legal_document_failed", "error", err, "doc_type", docType)
		response.Error(w, http.StatusInternalServerError, "failed to update legal document")
		return
	}

	response.JSON(w, http.StatusOK, toLegalDocumentResponseBody(doc))
}

func (h *Handler) GetTerms(w http.ResponseWriter, r *http.Request) {
	h.getLegalDocument(w, r, metadata.LegalDocTerms)
}

func (h *Handler) UpdateTerms(w http.ResponseWriter, r *http.Request) {
	h.updateLegalDocument(w, r, metadata.LegalDocTerms)
}

func (h *Handler) GetPrivacy(w http.ResponseWriter, r *http.Request) {
	h.getLegalDocument(w, r, metadata.LegalDocPrivacy)
}

func (h *Handler) UpdatePrivacy(w http.ResponseWriter, r *http.Request) {
	h.updateLegalDocument(w, r, metadata.LegalDocPrivacy)
}

func extractBearerToken(r *http.Request) string {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if len(auth) > len(prefix) && auth[:len(prefix)] == prefix {
		return auth[len(prefix):]
	}
	return ""
}

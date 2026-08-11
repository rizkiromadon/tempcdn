package admin

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/tempcdn/tempcdn/internal/metadata"
	"github.com/tempcdn/tempcdn/internal/response"
)

type Handler struct {
	service *Service
	logger  *slog.Logger
}

func NewHandler(service *Service, logger *slog.Logger) *Handler {
	return &Handler{service: service, logger: logger}
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

// Login handles POST /api/v1/admin/login. On success it returns the
// plaintext session token in the JSON body (not a cookie): this API is
// intended for a separate admin dashboard client, which is responsible for
// storing the token and sending it back as
// "Authorization: Bearer <token>" on subsequent requests (see
// RequireAdminSession).
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

// Logout handles POST /api/v1/admin/logout. Always behind
// RequireAdminSession, so a valid token is guaranteed to have been present
// to reach here; this just revokes it.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	token := extractBearerToken(r)
	if err := h.service.Logout(r.Context(), token); err != nil {
		h.logger.Error("admin_logout_failed", "error", err)
		response.Error(w, http.StatusInternalServerError, "failed to log out")
		return
	}
	response.JSON(w, http.StatusOK, map[string]bool{"logged_out": true})
}

// Me handles GET /api/v1/admin/me: lets the dashboard client verify a
// stored token is still valid and fetch the current admin's identity,
// e.g. on page load. Always behind RequireAdminSession.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	session, ok := SessionFromContext(r.Context())
	if !ok {
		// Unreachable when this handler is mounted behind
		// RequireAdminSession, which always populates the context on the
		// success path it lets through. Guarded anyway so a future
		// routing mistake fails as a clear 500, not a nil-pointer panic.
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

// CreateAPIKey handles POST /api/v1/admin/api-keys. On success it returns
// the plaintext key in the JSON body exactly once - like Login's session
// token, this is the only time the plaintext is ever visible; only its
// hash is persisted (see metadata.APIKey.TokenHash). Always behind
// RequireAdminSession.
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

// ListAPIKeys handles GET /api/v1/admin/api-keys: returns every API key
// (active and revoked) with metadata only - the plaintext key is never
// retrievable after creation. Always behind RequireAdminSession.
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

// RevokeAPIKey handles DELETE /api/v1/admin/api-keys/{id}. Idempotent:
// revoking an already-revoked or nonexistent key still returns success.
// Always behind RequireAdminSession.
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

// extractBearerToken reads the session token from the Authorization
// header ("Bearer <token>"). Same header RequireAdminSession expects.
func extractBearerToken(r *http.Request) string {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if len(auth) > len(prefix) && auth[:len(prefix)] == prefix {
		return auth[len(prefix):]
	}
	return ""
}

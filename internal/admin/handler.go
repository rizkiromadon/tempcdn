package admin

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

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

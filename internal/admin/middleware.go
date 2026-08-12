package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/rizkiromadon/tempcdn/internal/response"
)

type contextKey int

const sessionContextKey contextKey = 0

func SessionFromContext(ctx context.Context) (*Session, bool) {
	session, ok := ctx.Value(sessionContextKey).(*Session)
	return session, ok
}

func VerifyAPIKeyOrAdminSession(ctx context.Context, credential string, service *Service, logger *slog.Logger) bool {
	if credential == "" {
		return false
	}
	if _, err := service.VerifySession(ctx, credential); err == nil {
		return true
	} else if !errors.Is(err, ErrSessionInvalid) {
		logger.Error("api_key_admin_session_verify_failed", "error", err)
	}
	if _, err := service.VerifyAPIKey(ctx, credential); err == nil {
		return true
	} else if !errors.Is(err, ErrAPIKeyInvalid) {
		logger.Error("api_key_verify_failed", "error", err)
	}
	return false
}

func RequireAdminSession(service *Service, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractBearerToken(r)
			session, err := service.VerifySession(r.Context(), token)
			if errors.Is(err, ErrSessionInvalid) {
				response.Error(w, http.StatusUnauthorized, "missing or invalid session token")
				return
			}
			if err != nil {
				logger.Error("admin_session_verify_failed", "error", err)
				response.Error(w, http.StatusInternalServerError, "failed to verify session")
				return
			}

			ctx := context.WithValue(r.Context(), sessionContextKey, session)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

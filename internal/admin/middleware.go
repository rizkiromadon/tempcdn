package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/tempcdn/tempcdn/internal/response"
)

type contextKey int

const sessionContextKey contextKey = 0

// SessionFromContext retrieves the *Session stored by RequireAdminSession.
// Only ever populated on requests that passed authentication, so callers
// behind that middleware can assume ok is true (see Handler.Me for the
// defensive check anyway).
func SessionFromContext(ctx context.Context) (*Session, bool) {
	session, ok := ctx.Value(sessionContextKey).(*Session)
	return session, ok
}

// RequireAdminSession is standard chi-style middleware: it rejects any
// request without a valid "Authorization: Bearer <token>" session token,
// and on success stores the resolved *Session in the request context for
// downstream handlers (see SessionFromContext).
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

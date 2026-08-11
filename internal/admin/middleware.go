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

// VerifyAPIKeyOrAdminSession checks the given credential (usually read
// from a Bearer Authorization header or a custom header) against either a
// valid admin session or a valid, non-revoked API key, in that order.
// Returns nil if either check succeeds. This is the shared authorization
// check behind server-to-server endpoints like /metrics that need to
// accept both an operator's logged-in session and a long-lived API key
// (e.g. for a Prometheus scrape config that can't log in interactively).
// Unlike RequireAdminSession, this is a plain function rather than chi
// middleware, since callers like metricsAuth need to run it conditionally
// (only once an API-key-or-session gate has actually been enabled) rather
// than unconditionally on every request.
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

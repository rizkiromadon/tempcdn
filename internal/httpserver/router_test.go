package httpserver

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// testLogger returns a discard-output logger for tests that need a
// non-nil *slog.Logger to construct a Router but don't care about its
// output.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// These tests cover the /files/{id} preflight dispatch bug: a preflight
// (OPTIONS) always answered with the permissive GET CORS policy
// (Allow-Methods: GET, OPTIONS), which does not include DELETE. Per the CORS
// spec, browsers reject a preflight - and therefore the real request - when
// the requested method isn't listed in Access-Control-Allow-Methods, which
// broke browser-based delete for every origin, including the configured
// ALLOWED_ORIGIN. filePreflightCORS must instead answer according to the
// browser-sent Access-Control-Request-Method header.

func TestFilePreflightCORSAnswersGETPreflightWithPermissivePolicy(t *testing.T) {
	getCORS := CORS("*", "GET, OPTIONS")
	deleteCORS := CORS("https://app.example.com", "DELETE, OPTIONS")
	noop := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	handler := filePreflightCORS(getCORS, deleteCORS, noop)

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/files/abc123", nil)
	req.Header.Set("Origin", "https://some-other-site.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("expected Allow-Origin '*' for GET preflight, got %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "GET, OPTIONS" {
		t.Errorf("expected Allow-Methods 'GET, OPTIONS', got %q", got)
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204 No Content, got %d", rec.Code)
	}
}

func TestFilePreflightCORSAnswersDELETEPreflightWithStrictPolicyMatchingAllowedOrigin(t *testing.T) {
	getCORS := CORS("*", "GET, OPTIONS")
	deleteCORS := CORS("https://app.example.com", "DELETE, OPTIONS")
	noop := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	handler := filePreflightCORS(getCORS, deleteCORS, noop)

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/files/abc123", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", http.MethodDelete)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	// The regression: this used to come back "*" / "GET, OPTIONS", which
	// does not list DELETE, so the browser would refuse to send the real
	// DELETE request at all - even from the legitimate ALLOWED_ORIGIN.
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("expected Allow-Origin 'https://app.example.com' for DELETE preflight, got %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "DELETE, OPTIONS" {
		t.Errorf("expected Allow-Methods 'DELETE, OPTIONS' (must include DELETE), got %q", got)
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204 No Content, got %d", rec.Code)
	}
}

func TestFilePreflightCORSFallsBackToPermissivePolicyWhenRequestMethodMissing(t *testing.T) {
	getCORS := CORS("*", "GET, OPTIONS")
	deleteCORS := CORS("https://app.example.com", "DELETE, OPTIONS")
	noop := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	handler := filePreflightCORS(getCORS, deleteCORS, noop)

	// A bare OPTIONS request with no Access-Control-Request-Method (e.g. a
	// manual health-check style call, not a browser CORS preflight) should
	// not accidentally get the strict, origin-locked policy.
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/files/abc123", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("expected Allow-Origin '*' fallback, got %q", got)
	}
}

func TestCORSMiddlewareSetsHeadersAndShortCircuitsOptions(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	handler := CORS("https://app.example.com", "DELETE, OPTIONS")(next)

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/files/abc123", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if called {
		t.Error("expected next handler not to be called for OPTIONS request")
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204 No Content, got %d", rec.Code)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("expected Vary: Origin, got %q", got)
	}
}

func TestCORSMiddlewareCallsNextForNonOptionsRequest(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := CORS("https://app.example.com", "DELETE, OPTIONS")(next)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/files/abc123", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Error("expected next handler to be called for DELETE request")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("expected Allow-Origin 'https://app.example.com', got %q", got)
	}
}

// TestHealthzAllowsHead guards against a real gap: chi does not
// auto-register HEAD for a route registered with Get (unlike
// net/http.ServeMux), so uptime/monitoring checks that poll with HEAD -
// specifically to avoid pulling a response body on every check - would get
// a 405 unless HEAD is registered explicitly alongside GET.
func TestHealthzAllowsHead(t *testing.T) {
	router := NewRouter(RouterDependencies{
		Logger:        testLogger(),
		AllowedOrigin: "https://app.example.com",
	})

	req := httptest.NewRequest(http.MethodHead, "/healthz", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK for HEAD /healthz, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", got)
	}
	// Per the HTTP spec a HEAD response must carry no body, even though
	// GET /healthz on the same route returns one.
	if rec.Body.Len() != 0 {
		t.Errorf("expected empty body for HEAD /healthz, got %q", rec.Body.String())
	}
}

func TestHealthzGetStillReturnsBody(t *testing.T) {
	router := NewRouter(RouterDependencies{
		Logger:        testLogger(),
		AllowedOrigin: "https://app.example.com",
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK for GET /healthz, got %d", rec.Code)
	}
	if got := rec.Body.String(); got != `{"status":"ok"}` {
		t.Errorf("expected health check body, got %q", got)
	}
}

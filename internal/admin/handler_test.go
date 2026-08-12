package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rizkiromadon/tempcdn/internal/metadata"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHandlerLoginReturns200AndTokenOnValidCredentials(t *testing.T) {
	repo := newFakeRepository()
	newTestAdmin(t, repo, "alice", "correct-horse-battery-staple")
	svc := NewService(repo)
	handler := NewHandler(svc, testLogger())

	body, _ := json.Marshal(loginRequestBody{Username: "alice", Password: "correct-horse-battery-staple"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.Login(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp loginResponseBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Token == "" {
		t.Error("expected a non-empty token in the response")
	}
	if resp.Username != "alice" {
		t.Errorf("expected username 'alice', got %q", resp.Username)
	}
}

func TestHandlerLoginReturns401OnInvalidCredentials(t *testing.T) {
	repo := newFakeRepository()
	newTestAdmin(t, repo, "alice", "correct-horse-battery-staple")
	svc := NewService(repo)
	handler := NewHandler(svc, testLogger())

	body, _ := json.Marshal(loginRequestBody{Username: "alice", Password: "wrong-password"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.Login(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestHandlerLoginReturns400OnMalformedBody(t *testing.T) {
	repo := newFakeRepository()
	svc := NewService(repo)
	handler := NewHandler(svc, testLogger())

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/login", bytes.NewReader([]byte("not json")))
	rec := httptest.NewRecorder()

	handler.Login(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for malformed body, got %d", rec.Code)
	}
}

func TestRequireAdminSessionRejectsRequestWithNoToken(t *testing.T) {
	repo := newFakeRepository()
	svc := NewService(repo)

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	protected := RequireAdminSession(svc, testLogger())(next)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/me", nil)
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)

	if called {
		t.Error("expected next handler not to be called without a token")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestRequireAdminSessionAllowsRequestWithValidTokenAndPopulatesContext(t *testing.T) {
	repo := newFakeRepository()
	newTestAdmin(t, repo, "alice", "correct-horse-battery-staple")
	svc := NewService(repo)

	loginResult, err := svc.Login(context.Background(), "alice", "correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}

	var gotSession *Session
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, ok := SessionFromContext(r.Context())
		if ok {
			gotSession = session
		}
		w.WriteHeader(http.StatusOK)
	})
	protected := RequireAdminSession(svc, testLogger())(next)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/me", nil)
	req.Header.Set("Authorization", "Bearer "+loginResult.Token)
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if gotSession == nil {
		t.Fatal("expected session to be populated in request context")
	}
	if gotSession.Admin.Username != "alice" {
		t.Errorf("expected admin 'alice' in context, got %q", gotSession.Admin.Username)
	}
}

func TestRequireAdminSessionRejectsMalformedAuthorizationHeader(t *testing.T) {
	repo := newFakeRepository()
	svc := NewService(repo)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	})
	protected := RequireAdminSession(svc, testLogger())(next)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/me", nil)
	req.Header.Set("Authorization", "not-a-bearer-token")
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for malformed Authorization header, got %d", rec.Code)
	}
}

func TestHandlerLogoutRevokesSession(t *testing.T) {
	repo := newFakeRepository()
	newTestAdmin(t, repo, "alice", "correct-horse-battery-staple")
	svc := NewService(repo)
	handler := NewHandler(svc, testLogger())

	loginResult, err := svc.Login(context.Background(), "alice", "correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/logout", nil)
	req.Header.Set("Authorization", "Bearer "+loginResult.Token)
	rec := httptest.NewRecorder()

	handler.Logout(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if _, err := svc.VerifySession(context.Background(), loginResult.Token); err == nil {
		t.Error("expected session to be invalid after logout")
	}
}

func TestHandlerMeReturnsCurrentAdminUsername(t *testing.T) {
	repo := newFakeRepository()
	newTestAdmin(t, repo, "alice", "correct-horse-battery-staple")
	svc := NewService(repo)
	handler := NewHandler(svc, testLogger())

	loginResult, err := svc.Login(context.Background(), "alice", "correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}

	session, err := svc.VerifySession(context.Background(), loginResult.Token)
	if err != nil {
		t.Fatalf("verify session failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/me", nil)
	ctx := context.WithValue(req.Context(), sessionContextKey, session)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	handler.Me(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body["username"] != "alice" {
		t.Errorf("expected username 'alice', got %q", body["username"])
	}
}

func TestHandlerGetUploadSettingsReturnsCurrentValues(t *testing.T) {
	repo := newFakeRepository()
	seedTestUploadSettings(t, repo)
	svc := NewService(repo)
	handler := NewHandler(svc, testLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/upload-settings", nil)
	rec := httptest.NewRecorder()

	handler.GetUploadSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body uploadSettingsResponseBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body.MaxUploadSizeMB != 100 {
		t.Errorf("expected max_upload_size_mb=100, got %d", body.MaxUploadSizeMB)
	}
}

func TestHandlerUpdateUploadSettingsPersistsChangeAndInvokesCallback(t *testing.T) {
	repo := newFakeRepository()
	seedTestUploadSettings(t, repo)
	newTestAdmin(t, repo, "alice", "correct-horse-battery-staple")
	svc := NewService(repo)
	handler := NewHandler(svc, testLogger())

	loginResult, err := svc.Login(context.Background(), "alice", "correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	session, err := svc.VerifySession(context.Background(), loginResult.Token)
	if err != nil {
		t.Fatalf("verify session failed: %v", err)
	}

	var callbackSettings *metadata.UploadSettings
	handler.SetUploadSettingsUpdatedCallback(func(s *metadata.UploadSettings) {
		callbackSettings = s
	})

	body, _ := json.Marshal(updateUploadSettingsRequestBody{
		MaxUploadSizeMB:   500,
		AllowedMimeTypes:  []string{"image/*", "video/*"},
		BlockedExtensions: []string{".exe"},
	})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/upload-settings", bytes.NewReader(body))
	ctx := context.WithValue(req.Context(), sessionContextKey, session)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	handler.UpdateUploadSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp uploadSettingsResponseBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.MaxUploadSizeMB != 500 {
		t.Errorf("expected max_upload_size_mb=500 in response, got %d", resp.MaxUploadSizeMB)
	}

	if callbackSettings == nil {
		t.Fatal("expected onUploadSettingsUpdated callback to be invoked")
	}
	if callbackSettings.MaxUploadSizeMB != 500 {
		t.Errorf("expected callback to receive max_upload_size_mb=500, got %d", callbackSettings.MaxUploadSizeMB)
	}

	stored, err := svc.GetUploadSettings(context.Background())
	if err != nil {
		t.Fatalf("failed to re-read settings: %v", err)
	}
	if stored.MaxUploadSizeMB != 500 {
		t.Errorf("expected persisted max_upload_size_mb=500, got %d", stored.MaxUploadSizeMB)
	}
}

func TestHandlerUpdateUploadSettingsReturns400OnInvalidInput(t *testing.T) {
	repo := newFakeRepository()
	seedTestUploadSettings(t, repo)
	newTestAdmin(t, repo, "alice", "correct-horse-battery-staple")
	svc := NewService(repo)
	handler := NewHandler(svc, testLogger())

	loginResult, err := svc.Login(context.Background(), "alice", "correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	session, err := svc.VerifySession(context.Background(), loginResult.Token)
	if err != nil {
		t.Fatalf("verify session failed: %v", err)
	}

	body, _ := json.Marshal(updateUploadSettingsRequestBody{
		MaxUploadSizeMB:   0,
		AllowedMimeTypes:  []string{"image/*"},
		BlockedExtensions: nil,
	})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/upload-settings", bytes.NewReader(body))
	ctx := context.WithValue(req.Context(), sessionContextKey, session)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	handler.UpdateUploadSettings(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlerUpdateUploadSettingsReturns400OnMalformedBody(t *testing.T) {
	repo := newFakeRepository()
	seedTestUploadSettings(t, repo)
	svc := NewService(repo)
	handler := NewHandler(svc, testLogger())

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/upload-settings", bytes.NewReader([]byte("not json")))
	rec := httptest.NewRecorder()

	handler.UpdateUploadSettings(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for malformed body, got %d", rec.Code)
	}
}

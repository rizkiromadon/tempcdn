package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rizkiromadon/tempcdn/internal/admin"
	"github.com/rizkiromadon/tempcdn/internal/metadata"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

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

func TestMetricsOpenWhenNoAdminServiceConfigured(t *testing.T) {
	router := NewRouter(RouterDependencies{
		Logger:        testLogger(),
		AllowedOrigin: "https://app.example.com",

	})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK for GET /metrics with no admin service configured, got %d", rec.Code)
	}
}

func TestMetricsRejectsRequestWithNoCredentials(t *testing.T) {
	router := NewRouter(RouterDependencies{
		Logger:        testLogger(),
		AllowedOrigin: "https://app.example.com",
		AdminService:  admin.NewService(newMetricsFakeRepository()),
	})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for GET /metrics with no credentials, got %d", rec.Code)
	}
}

func TestMetricsAcceptsValidAPIKey(t *testing.T) {
	repo := newMetricsFakeRepository()
	adminService := admin.NewService(repo)
	ctx := context.Background()
	result, err := adminService.CreateAPIKey(ctx, "prometheus-prod")
	if err != nil {
		t.Fatalf("failed to create api key: %v", err)
	}

	router := NewRouter(RouterDependencies{
		Logger:        testLogger(),
		AllowedOrigin: "https://app.example.com",
		AdminService:  adminService,
	})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("X-Metrics-Token", result.Key)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK for GET /metrics with valid API key via X-Metrics-Token, got %d", rec.Code)
	}

	reqBearer := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	reqBearer.Header.Set("Authorization", "Bearer "+result.Key)
	recBearer := httptest.NewRecorder()
	router.ServeHTTP(recBearer, reqBearer)

	if recBearer.Code != http.StatusOK {
		t.Errorf("expected 200 OK for GET /metrics with valid API key via Bearer, got %d", recBearer.Code)
	}
}

func TestMetricsRejectsRevokedAPIKey(t *testing.T) {
	repo := newMetricsFakeRepository()
	adminService := admin.NewService(repo)
	ctx := context.Background()
	result, err := adminService.CreateAPIKey(ctx, "prometheus-prod")
	if err != nil {
		t.Fatalf("failed to create api key: %v", err)
	}
	if err := adminService.RevokeAPIKey(ctx, result.Record.ID); err != nil {
		t.Fatalf("failed to revoke api key: %v", err)
	}

	router := NewRouter(RouterDependencies{
		Logger:        testLogger(),
		AllowedOrigin: "https://app.example.com",
		AdminService:  adminService,
	})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("X-Metrics-Token", result.Key)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for GET /metrics with revoked API key, got %d", rec.Code)
	}
}

func TestMetricsRejectsInvalidAPIKey(t *testing.T) {
	router := NewRouter(RouterDependencies{
		Logger:        testLogger(),
		AllowedOrigin: "https://app.example.com",
		AdminService:  admin.NewService(newMetricsFakeRepository()),
	})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("X-Metrics-Token", "tcdn_wrong-key")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for GET /metrics with invalid API key, got %d", rec.Code)
	}
}

type metricsFakeRepository struct {
	apiKeysByHash map[string]*metadata.APIKey
	apiKeysByID   map[string]*metadata.APIKey
}

func newMetricsFakeRepository() *metricsFakeRepository {
	return &metricsFakeRepository{
		apiKeysByHash: make(map[string]*metadata.APIKey),
		apiKeysByID:   make(map[string]*metadata.APIKey),
	}
}

func (f *metricsFakeRepository) Migrate(ctx context.Context) error { panic("not implemented") }
func (f *metricsFakeRepository) Insert(ctx context.Context, record *metadata.FileRecord) error {
	panic("not implemented")
}
func (f *metricsFakeRepository) FindActiveByChecksum(ctx context.Context, checksum string, now time.Time) (*metadata.FileRecord, error) {
	panic("not implemented")
}
func (f *metricsFakeRepository) FindByID(ctx context.Context, id string) (*metadata.FileRecord, error) {
	panic("not implemented")
}
func (f *metricsFakeRepository) DeleteByID(ctx context.Context, id string) error {
	panic("not implemented")
}
func (f *metricsFakeRepository) FindExpired(ctx context.Context, before time.Time, limit int) ([]*metadata.FileRecord, error) {
	panic("not implemented")
}
func (f *metricsFakeRepository) Stats(ctx context.Context, now time.Time) (*metadata.Stats, error) {
	panic("not implemented")
}
func (f *metricsFakeRepository) Heartbeat(ctx context.Context, nodeID, hostname string, startedAt, now time.Time) error {
	panic("not implemented")
}
func (f *metricsFakeRepository) MarkStaleOffline(ctx context.Context, before, now time.Time) ([]string, error) {
	panic("not implemented")
}
func (f *metricsFakeRepository) ListNodeStatus(ctx context.Context) ([]*metadata.NodeStatus, error) {
	panic("not implemented")
}
func (f *metricsFakeRepository) InsertAdmin(ctx context.Context, admin *metadata.Admin) error {
	panic("not implemented")
}
func (f *metricsFakeRepository) FindAdminByUsername(ctx context.Context, username string) (*metadata.Admin, error) {
	return nil, metadata.ErrAdminNotFound
}
func (f *metricsFakeRepository) FindAdminByID(ctx context.Context, id string) (*metadata.Admin, error) {
	return nil, metadata.ErrAdminNotFound
}
func (f *metricsFakeRepository) CountAdmins(ctx context.Context) (int64, error) { return 0, nil }
func (f *metricsFakeRepository) TouchAdminLastLogin(ctx context.Context, adminID string, now time.Time) error {
	panic("not implemented")
}
func (f *metricsFakeRepository) InsertAdminSession(ctx context.Context, session *metadata.AdminSession) error {
	panic("not implemented")
}
func (f *metricsFakeRepository) FindAdminSessionByTokenHash(ctx context.Context, tokenHash string) (*metadata.AdminSession, error) {
	return nil, metadata.ErrAdminSessionNotFound
}
func (f *metricsFakeRepository) TouchAdminSession(ctx context.Context, tokenHash string, now time.Time) error {
	panic("not implemented")
}
func (f *metricsFakeRepository) DeleteAdminSession(ctx context.Context, tokenHash string) error {
	panic("not implemented")
}
func (f *metricsFakeRepository) DeleteExpiredAdminSessions(ctx context.Context, before time.Time) error {
	panic("not implemented")
}
func (f *metricsFakeRepository) InsertAPIKey(ctx context.Context, key *metadata.APIKey) error {
	copyOfKey := *key
	f.apiKeysByHash[key.TokenHash] = &copyOfKey
	f.apiKeysByID[key.ID] = &copyOfKey
	return nil
}
func (f *metricsFakeRepository) FindAPIKeyByTokenHash(ctx context.Context, tokenHash string) (*metadata.APIKey, error) {
	key, exists := f.apiKeysByHash[tokenHash]
	if !exists {
		return nil, metadata.ErrAPIKeyNotFound
	}
	copyOfKey := *key
	return &copyOfKey, nil
}
func (f *metricsFakeRepository) ListAPIKeys(ctx context.Context) ([]*metadata.APIKey, error) {
	keys := make([]*metadata.APIKey, 0, len(f.apiKeysByID))
	for _, key := range f.apiKeysByID {
		copyOfKey := *key
		keys = append(keys, &copyOfKey)
	}
	return keys, nil
}
func (f *metricsFakeRepository) TouchAPIKey(ctx context.Context, id string, now time.Time) error {
	key, exists := f.apiKeysByID[id]
	if !exists {
		return nil
	}
	key.LastUsedAt = &now
	if hashed, ok := f.apiKeysByHash[key.TokenHash]; ok {
		hashed.LastUsedAt = &now
	}
	return nil
}
func (f *metricsFakeRepository) RevokeAPIKey(ctx context.Context, id string, now time.Time) error {
	key, exists := f.apiKeysByID[id]
	if !exists {
		return nil
	}
	key.RevokedAt = &now
	if hashed, ok := f.apiKeysByHash[key.TokenHash]; ok {
		hashed.RevokedAt = &now
	}
	return nil
}
func (f *metricsFakeRepository) GetUploadSettings(ctx context.Context) (*metadata.UploadSettings, error) {
	panic("not implemented")
}
func (f *metricsFakeRepository) SeedUploadSettingsIfMissing(ctx context.Context, settings *metadata.UploadSettings) error {
	panic("not implemented")
}
func (f *metricsFakeRepository) UpdateUploadSettings(ctx context.Context, settings *metadata.UploadSettings, updatedBy string, now time.Time) error {
	panic("not implemented")
}
func (f *metricsFakeRepository) Close() error { panic("not implemented") }

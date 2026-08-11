package stats

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tempcdn/tempcdn/internal/metadata"
)

// fakeRepository is a minimal metadata.Repository stub - only Stats is
// exercised by this handler, so every other method is unused and panics if
// ever called, to make an accidental dependency on them fail loudly.
type fakeRepository struct {
	stats    *metadata.Stats
	statsErr error
}

func (f *fakeRepository) Migrate(ctx context.Context) error { panic("not implemented") }
func (f *fakeRepository) Insert(ctx context.Context, record *metadata.FileRecord) error {
	panic("not implemented")
}
func (f *fakeRepository) FindActiveByChecksum(ctx context.Context, checksum string, now time.Time) (*metadata.FileRecord, error) {
	panic("not implemented")
}
func (f *fakeRepository) FindByID(ctx context.Context, id string) (*metadata.FileRecord, error) {
	panic("not implemented")
}
func (f *fakeRepository) DeleteByID(ctx context.Context, id string) error {
	panic("not implemented")
}
func (f *fakeRepository) FindExpired(ctx context.Context, before time.Time, limit int) ([]*metadata.FileRecord, error) {
	panic("not implemented")
}
func (f *fakeRepository) Stats(ctx context.Context, now time.Time) (*metadata.Stats, error) {
	return f.stats, f.statsErr
}
func (f *fakeRepository) Heartbeat(ctx context.Context, nodeID, hostname string, startedAt, now time.Time) error {
	panic("not implemented")
}
func (f *fakeRepository) MarkStaleOffline(ctx context.Context, before, now time.Time) ([]string, error) {
	panic("not implemented")
}
func (f *fakeRepository) ListNodeStatus(ctx context.Context) ([]*metadata.NodeStatus, error) {
	panic("not implemented")
}
func (f *fakeRepository) InsertAdmin(ctx context.Context, admin *metadata.Admin) error {
	panic("not implemented")
}
func (f *fakeRepository) FindAdminByUsername(ctx context.Context, username string) (*metadata.Admin, error) {
	panic("not implemented")
}
func (f *fakeRepository) FindAdminByID(ctx context.Context, id string) (*metadata.Admin, error) {
	panic("not implemented")
}
func (f *fakeRepository) CountAdmins(ctx context.Context) (int64, error) {
	panic("not implemented")
}
func (f *fakeRepository) TouchAdminLastLogin(ctx context.Context, adminID string, now time.Time) error {
	panic("not implemented")
}
func (f *fakeRepository) InsertAdminSession(ctx context.Context, session *metadata.AdminSession) error {
	panic("not implemented")
}
func (f *fakeRepository) FindAdminSessionByTokenHash(ctx context.Context, tokenHash string) (*metadata.AdminSession, error) {
	panic("not implemented")
}
func (f *fakeRepository) TouchAdminSession(ctx context.Context, tokenHash string, now time.Time) error {
	panic("not implemented")
}
func (f *fakeRepository) DeleteAdminSession(ctx context.Context, tokenHash string) error {
	panic("not implemented")
}
func (f *fakeRepository) DeleteExpiredAdminSessions(ctx context.Context, before time.Time) error {
	panic("not implemented")
}
func (f *fakeRepository) InsertAPIKey(ctx context.Context, key *metadata.APIKey) error {
	panic("not implemented")
}
func (f *fakeRepository) FindAPIKeyByTokenHash(ctx context.Context, tokenHash string) (*metadata.APIKey, error) {
	panic("not implemented")
}
func (f *fakeRepository) ListAPIKeys(ctx context.Context) ([]*metadata.APIKey, error) {
	panic("not implemented")
}
func (f *fakeRepository) TouchAPIKey(ctx context.Context, id string, now time.Time) error {
	panic("not implemented")
}
func (f *fakeRepository) RevokeAPIKey(ctx context.Context, id string, now time.Time) error {
	panic("not implemented")
}
func (f *fakeRepository) GetUploadSettings(ctx context.Context) (*metadata.UploadSettings, error) {
	panic("not implemented")
}
func (f *fakeRepository) SeedUploadSettingsIfMissing(ctx context.Context, settings *metadata.UploadSettings) error {
	panic("not implemented")
}
func (f *fakeRepository) UpdateUploadSettings(ctx context.Context, settings *metadata.UploadSettings, updatedBy string, now time.Time) error {
	panic("not implemented")
}
func (f *fakeRepository) Close() error { panic("not implemented") }

func newTestCounters() (uploads, bytesTotal, errorsTotal prometheus.Counter) {
	return prometheus.NewCounter(prometheus.CounterOpts{Name: "test_uploads_total"}),
		prometheus.NewCounter(prometheus.CounterOpts{Name: "test_upload_bytes_total"}),
		prometheus.NewCounter(prometheus.CounterOpts{Name: "test_upload_errors_total"})
}

func TestServeHTTPReturnsActiveAndLifetimeFigures(t *testing.T) {
	repo := &fakeRepository{
		stats: &metadata.Stats{
			ActiveFileCount:      2,
			ActiveBytes:          3000,
			ContentTypeBreakdown: map[string]int64{"image": 2},
		},
	}
	uploadsTotal, uploadBytesTotal, uploadErrorsTotal := newTestCounters()
	uploadsTotal.Add(10)
	uploadBytesTotal.Add(123456)
	uploadErrorsTotal.Add(2)

	handler := NewHandler(repo, uploadsTotal, uploadBytesTotal, uploadErrorsTotal)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body Response
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if body.ActiveFileCount != 2 {
		t.Errorf("expected active_file_count 2, got %d", body.ActiveFileCount)
	}
	if body.ActiveBytes != 3000 {
		t.Errorf("expected active_bytes 3000, got %d", body.ActiveBytes)
	}
	if body.AverageFileBytes != 1500 {
		t.Errorf("expected average_file_bytes 1500 (3000/2), got %d", body.AverageFileBytes)
	}
	if body.ContentTypeBreakdown["image"] != 2 {
		t.Errorf("expected content_type_breakdown[image]=2, got %d", body.ContentTypeBreakdown["image"])
	}
	if body.LifetimeUploadsTotal != 10 {
		t.Errorf("expected lifetime_uploads_total 10, got %d", body.LifetimeUploadsTotal)
	}
	if body.LifetimeUploadBytesTotal != 123456 {
		t.Errorf("expected lifetime_upload_bytes_total 123456, got %d", body.LifetimeUploadBytesTotal)
	}
	if body.LifetimeUploadErrorsTotal != 2 {
		t.Errorf("expected lifetime_upload_errors_total 2, got %d", body.LifetimeUploadErrorsTotal)
	}
	if body.GeneratedAt == "" {
		t.Error("expected generated_at to be set")
	}
}

// TestServeHTTPAverageIsZeroNotDivideByZeroWhenNoActiveFiles guards the
// explicit zero-file branch in ServeHTTP: without it, ActiveBytes /
// ActiveFileCount would panic with an integer divide-by-zero the moment the
// CDN has no active files (e.g. right after startup, or once everything has
// expired).
func TestServeHTTPAverageIsZeroNotDivideByZeroWhenNoActiveFiles(t *testing.T) {
	repo := &fakeRepository{
		stats: &metadata.Stats{
			ActiveFileCount:      0,
			ActiveBytes:          0,
			ContentTypeBreakdown: map[string]int64{},
		},
	}
	uploadsTotal, uploadBytesTotal, uploadErrorsTotal := newTestCounters()

	handler := NewHandler(repo, uploadsTotal, uploadBytesTotal, uploadErrorsTotal)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body Response
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body.AverageFileBytes != 0 {
		t.Errorf("expected average_file_bytes 0 when no active files, got %d", body.AverageFileBytes)
	}
}

func TestServeHTTPReturns500WhenRepositoryStatsFails(t *testing.T) {
	repo := &fakeRepository{statsErr: context.DeadlineExceeded}
	uploadsTotal, uploadBytesTotal, uploadErrorsTotal := newTestCounters()

	handler := NewHandler(repo, uploadsTotal, uploadBytesTotal, uploadErrorsTotal)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 when repository.Stats fails, got %d", rec.Code)
	}
}

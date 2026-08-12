package legal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rizkiromadon/tempcdn/internal/metadata"
)

// fakeRepository implements metadata.LegalDocumentRepository only — that's
// all legal.Handler depends on, so the fake doesn't need to know about
// files, admins, or anything else.
type fakeRepository struct {
	docsByType map[string]*metadata.LegalDocument
	getErr     error
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{docsByType: make(map[string]*metadata.LegalDocument)}
}

func (f *fakeRepository) GetLegalDocument(ctx context.Context, docType string) (*metadata.LegalDocument, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	doc, exists := f.docsByType[docType]
	if !exists {
		return nil, metadata.ErrLegalDocumentNotFound
	}
	copyOfDoc := *doc
	return &copyOfDoc, nil
}

func (f *fakeRepository) SeedLegalDocumentIfMissing(ctx context.Context, doc *metadata.LegalDocument) error {
	if _, exists := f.docsByType[doc.DocType]; exists {
		return nil
	}
	copyOfDoc := *doc
	f.docsByType[doc.DocType] = &copyOfDoc
	return nil
}

func (f *fakeRepository) UpdateLegalDocument(ctx context.Context, docType, content, updatedBy string, now time.Time) (*metadata.LegalDocument, error) {
	updated := &metadata.LegalDocument{DocType: docType, Content: content, UpdatedAt: now, UpdatedBy: &updatedBy}
	copyOfDoc := *updated
	f.docsByType[docType] = &copyOfDoc
	return updated, nil
}

func TestTermsReturnsCurrentContent(t *testing.T) {
	repo := newFakeRepository()
	now := time.Now().UTC()
	_ = repo.SeedLegalDocumentIfMissing(context.Background(), &metadata.LegalDocument{
		DocType:   metadata.LegalDocTerms,
		Content:   "Terms of service content.",
		UpdatedAt: now,
	})
	handler := NewHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/legal/terms", nil)
	rec := httptest.NewRecorder()
	handler.Terms(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body responseBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body.DocType != metadata.LegalDocTerms {
		t.Errorf("expected doc_type=%s, got %s", metadata.LegalDocTerms, body.DocType)
	}
	if body.Content != "Terms of service content." {
		t.Errorf("expected terms content, got %q", body.Content)
	}
}

func TestPrivacyReturnsCurrentContent(t *testing.T) {
	repo := newFakeRepository()
	now := time.Now().UTC()
	_ = repo.SeedLegalDocumentIfMissing(context.Background(), &metadata.LegalDocument{
		DocType:   metadata.LegalDocPrivacy,
		Content:   "Privacy policy content.",
		UpdatedAt: now,
	})
	handler := NewHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/legal/privacy", nil)
	rec := httptest.NewRecorder()
	handler.Privacy(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body responseBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body.Content != "Privacy policy content." {
		t.Errorf("expected privacy content, got %q", body.Content)
	}
}

func TestTermsReturns404WhenUnseeded(t *testing.T) {
	repo := newFakeRepository()
	handler := NewHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/legal/terms", nil)
	rec := httptest.NewRecorder()
	handler.Terms(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPrivacyReturns500OnRepositoryError(t *testing.T) {
	repo := newFakeRepository()
	repo.getErr = context.DeadlineExceeded
	handler := NewHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/legal/privacy", nil)
	rec := httptest.NewRecorder()
	handler.Privacy(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

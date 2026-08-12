package metadata

import (
	"context"
	"errors"
	"time"
)

var ErrLegalDocumentNotFound = errors.New("legal document not found")

const (
	LegalDocTerms   = "terms"
	LegalDocPrivacy = "privacy"
)

// LegalDocument is a single admin-editable legal document (terms of
// service, privacy policy, ...), identified by DocType.
type LegalDocument struct {
	DocType   string    `json:"doc_type"`
	Content   string    `json:"content"`
	UpdatedAt time.Time `json:"updated_at"`

	UpdatedBy *string `json:"updated_by,omitempty"`
}

// LegalDocumentRepository is intentionally narrow: only what
// internal/legal actually needs, not "everything you could ever do with
// the legal_documents table".
type LegalDocumentRepository interface {
	GetLegalDocument(ctx context.Context, docType string) (*LegalDocument, error)
	SeedLegalDocumentIfMissing(ctx context.Context, doc *LegalDocument) error
	UpdateLegalDocument(ctx context.Context, docType, content, updatedBy string, now time.Time) (*LegalDocument, error)
}

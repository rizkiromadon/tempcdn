package metadata

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// This file implements LegalDocumentRepository on *PostgresRepository.
// It owns everything about the `legal_documents` table (one row per
// doc_type, e.g. "terms" / "privacy").

func (r *PostgresRepository) GetLegalDocument(ctx context.Context, docType string) (*LegalDocument, error) {
	const query = `
		SELECT doc_type, content, updated_at, updated_by
		FROM legal_documents
		WHERE doc_type = $1
	`
	row := r.pool.QueryRow(ctx, query, docType)
	doc, err := pgScanLegalDocumentRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrLegalDocumentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan legal document: %w", err)
	}
	return doc, nil
}

func (r *PostgresRepository) SeedLegalDocumentIfMissing(ctx context.Context, doc *LegalDocument) error {
	const query = `
		INSERT INTO legal_documents (doc_type, content, updated_at, updated_by)
		VALUES ($1, $2, $3, NULL)
		ON CONFLICT (doc_type) DO NOTHING
	`
	if _, err := r.pool.Exec(ctx, query, doc.DocType, doc.Content, doc.UpdatedAt); err != nil {
		return fmt.Errorf("seed legal document: %w", err)
	}
	return nil
}

func (r *PostgresRepository) UpdateLegalDocument(ctx context.Context, docType, content, updatedBy string, now time.Time) (*LegalDocument, error) {
	const query = `
		UPDATE legal_documents
		SET content = $1, updated_at = $2, updated_by = $3
		WHERE doc_type = $4
		RETURNING doc_type, content, updated_at, updated_by
	`
	row := r.pool.QueryRow(ctx, query, content, now, updatedBy, docType)
	doc, err := pgScanLegalDocumentRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrLegalDocumentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update legal document: %w", err)
	}
	return doc, nil
}

func pgScanLegalDocumentRow(scanner pgRowScanner) (*LegalDocument, error) {
	var doc LegalDocument
	var updatedBy *string
	err := scanner.Scan(
		&doc.DocType,
		&doc.Content,
		&doc.UpdatedAt,
		&updatedBy,
	)
	if err != nil {
		return nil, err
	}
	doc.UpdatedBy = updatedBy
	return &doc, nil
}

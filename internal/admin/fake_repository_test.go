package admin

import (
	"context"
	"time"

	"github.com/rizkiromadon/tempcdn/internal/metadata"
)

// fakeRepository implements admin.ServiceRepository only (AdminRepository +
// APIKeyRepository + UploadSettingsRepository) — nothing about files or
// nodes, because admin.Service never touches those. That's the payoff of
// depending on narrow interfaces: this fake is a fraction of the size it
// would be if it had to satisfy the full metadata.Repository.
type fakeRepository struct {
	adminsByUsername map[string]*metadata.Admin
	adminsByID       map[string]*metadata.Admin
	sessionsByHash   map[string]*metadata.AdminSession
	apiKeysByHash    map[string]*metadata.APIKey
	apiKeysByID      map[string]*metadata.APIKey
	uploadSettings   *metadata.UploadSettings
	legalDocuments   map[string]*metadata.LegalDocument

	insertAdminErr        error
	insertAdminSessionErr error
	insertAPIKeyErr       error
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{
		adminsByUsername: make(map[string]*metadata.Admin),
		adminsByID:       make(map[string]*metadata.Admin),
		sessionsByHash:   make(map[string]*metadata.AdminSession),
		apiKeysByHash:    make(map[string]*metadata.APIKey),
		apiKeysByID:      make(map[string]*metadata.APIKey),
		legalDocuments:   make(map[string]*metadata.LegalDocument),
	}
}

func (f *fakeRepository) InsertAdmin(ctx context.Context, admin *metadata.Admin) error {
	if f.insertAdminErr != nil {
		return f.insertAdminErr
	}
	if _, exists := f.adminsByUsername[admin.Username]; exists {
		return metadata.ErrAdminUsernameTaken
	}
	copyOfAdmin := *admin
	f.adminsByUsername[admin.Username] = &copyOfAdmin
	f.adminsByID[admin.ID] = &copyOfAdmin
	return nil
}

func (f *fakeRepository) FindAdminByUsername(ctx context.Context, username string) (*metadata.Admin, error) {
	admin, exists := f.adminsByUsername[username]
	if !exists {
		return nil, metadata.ErrAdminNotFound
	}
	copyOfAdmin := *admin
	return &copyOfAdmin, nil
}

func (f *fakeRepository) FindAdminByID(ctx context.Context, id string) (*metadata.Admin, error) {
	admin, exists := f.adminsByID[id]
	if !exists {
		return nil, metadata.ErrAdminNotFound
	}
	copyOfAdmin := *admin
	return &copyOfAdmin, nil
}

func (f *fakeRepository) CountAdmins(ctx context.Context) (int64, error) {
	return int64(len(f.adminsByID)), nil
}

func (f *fakeRepository) TouchAdminLastLogin(ctx context.Context, adminID string, now time.Time) error {
	admin, exists := f.adminsByID[adminID]
	if !exists {
		return nil
	}
	admin.LastLoginAt = &now
	f.adminsByUsername[admin.Username].LastLoginAt = &now
	return nil
}

func (f *fakeRepository) InsertAdminSession(ctx context.Context, session *metadata.AdminSession) error {
	if f.insertAdminSessionErr != nil {
		return f.insertAdminSessionErr
	}
	copyOfSession := *session
	f.sessionsByHash[session.TokenHash] = &copyOfSession
	return nil
}

func (f *fakeRepository) FindAdminSessionByTokenHash(ctx context.Context, tokenHash string) (*metadata.AdminSession, error) {
	session, exists := f.sessionsByHash[tokenHash]
	if !exists {
		return nil, metadata.ErrAdminSessionNotFound
	}
	copyOfSession := *session
	return &copyOfSession, nil
}

func (f *fakeRepository) TouchAdminSession(ctx context.Context, tokenHash string, now time.Time) error {
	session, exists := f.sessionsByHash[tokenHash]
	if !exists {
		return nil
	}
	session.LastUsedAt = now
	return nil
}

func (f *fakeRepository) DeleteAdminSession(ctx context.Context, tokenHash string) error {
	delete(f.sessionsByHash, tokenHash)
	return nil
}

func (f *fakeRepository) DeleteExpiredAdminSessions(ctx context.Context, before time.Time) error {
	for hash, session := range f.sessionsByHash {
		if !session.ExpiresAt.After(before) {
			delete(f.sessionsByHash, hash)
		}
	}
	return nil
}

func (f *fakeRepository) InsertAPIKey(ctx context.Context, key *metadata.APIKey) error {
	if f.insertAPIKeyErr != nil {
		return f.insertAPIKeyErr
	}
	copyOfKey := *key
	f.apiKeysByHash[key.TokenHash] = &copyOfKey
	f.apiKeysByID[key.ID] = &copyOfKey
	return nil
}

func (f *fakeRepository) FindAPIKeyByTokenHash(ctx context.Context, tokenHash string) (*metadata.APIKey, error) {
	key, exists := f.apiKeysByHash[tokenHash]
	if !exists {
		return nil, metadata.ErrAPIKeyNotFound
	}
	copyOfKey := *key
	return &copyOfKey, nil
}

func (f *fakeRepository) ListAPIKeys(ctx context.Context) ([]*metadata.APIKey, error) {
	keys := make([]*metadata.APIKey, 0, len(f.apiKeysByID))
	for _, key := range f.apiKeysByID {
		copyOfKey := *key
		keys = append(keys, &copyOfKey)
	}
	return keys, nil
}

func (f *fakeRepository) TouchAPIKey(ctx context.Context, id string, now time.Time) error {
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

func (f *fakeRepository) RevokeAPIKey(ctx context.Context, id string, now time.Time) error {
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

func (f *fakeRepository) GetUploadSettings(ctx context.Context) (*metadata.UploadSettings, error) {
	if f.uploadSettings == nil {
		return nil, metadata.ErrUploadSettingsNotFound
	}
	copyOfSettings := *f.uploadSettings
	return &copyOfSettings, nil
}

func (f *fakeRepository) SeedUploadSettingsIfMissing(ctx context.Context, settings *metadata.UploadSettings) error {
	if f.uploadSettings != nil {
		return nil
	}
	copyOfSettings := *settings
	f.uploadSettings = &copyOfSettings
	return nil
}

func (f *fakeRepository) UpdateUploadSettings(ctx context.Context, settings *metadata.UploadSettings, updatedBy string, now time.Time) error {
	if f.uploadSettings == nil {
		return metadata.ErrUploadSettingsNotFound
	}
	copyOfSettings := *settings
	copyOfSettings.UpdatedAt = now
	copyOfSettings.UpdatedBy = &updatedBy
	f.uploadSettings = &copyOfSettings
	return nil
}

func (f *fakeRepository) GetLegalDocument(ctx context.Context, docType string) (*metadata.LegalDocument, error) {
	doc, exists := f.legalDocuments[docType]
	if !exists {
		return nil, metadata.ErrLegalDocumentNotFound
	}
	copyOfDoc := *doc
	return &copyOfDoc, nil
}

func (f *fakeRepository) SeedLegalDocumentIfMissing(ctx context.Context, doc *metadata.LegalDocument) error {
	if _, exists := f.legalDocuments[doc.DocType]; exists {
		return nil
	}
	copyOfDoc := *doc
	f.legalDocuments[doc.DocType] = &copyOfDoc
	return nil
}

func (f *fakeRepository) UpdateLegalDocument(ctx context.Context, docType, content, updatedBy string, now time.Time) (*metadata.LegalDocument, error) {
	if _, exists := f.legalDocuments[docType]; !exists {
		return nil, metadata.ErrLegalDocumentNotFound
	}
	updated := &metadata.LegalDocument{
		DocType:   docType,
		Content:   content,
		UpdatedAt: now,
		UpdatedBy: &updatedBy,
	}
	copyOfDoc := *updated
	f.legalDocuments[docType] = &copyOfDoc
	return updated, nil
}

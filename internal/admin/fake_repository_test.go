package admin

import (
	"context"
	"time"

	"github.com/tempcdn/tempcdn/internal/metadata"
)

// fakeRepository is an in-memory metadata.Repository stub covering only
// the admin-related methods exercised by this package's tests. Every
// other method panics if called, so an accidental dependency on
// file/node-status behavior fails loudly rather than silently returning a
// zero value.
type fakeRepository struct {
	adminsByUsername map[string]*metadata.Admin
	adminsByID       map[string]*metadata.Admin
	sessionsByHash   map[string]*metadata.AdminSession

	insertAdminErr        error
	insertAdminSessionErr error
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{
		adminsByUsername: make(map[string]*metadata.Admin),
		adminsByID:       make(map[string]*metadata.Admin),
		sessionsByHash:   make(map[string]*metadata.AdminSession),
	}
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
func (f *fakeRepository) DeleteByID(ctx context.Context, id string) error { panic("not implemented") }
func (f *fakeRepository) FindExpired(ctx context.Context, before time.Time, limit int) ([]*metadata.FileRecord, error) {
	panic("not implemented")
}
func (f *fakeRepository) Stats(ctx context.Context, now time.Time) (*metadata.Stats, error) {
	panic("not implemented")
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

func (f *fakeRepository) Close() error { panic("not implemented") }

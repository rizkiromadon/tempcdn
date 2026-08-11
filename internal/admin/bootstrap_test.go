package admin

import (
	"context"
	"testing"

	"github.com/tempcdn/tempcdn/internal/metadata"
	"golang.org/x/crypto/bcrypt"
)

func TestBootstrapCreatesAdminWhenNoneExist(t *testing.T) {
	repo := newFakeRepository()

	err := Bootstrap(context.Background(), repo, BootstrapConfig{
		Username: "admin",
		Password: "at-least-12-characters",
	})
	if err != nil {
		t.Fatalf("expected bootstrap to succeed, got: %v", err)
	}

	acct, exists := repo.adminsByUsername["admin"]
	if !exists {
		t.Fatal("expected an admin account named 'admin' to be created")
	}
	if bcrypt.CompareHashAndPassword([]byte(acct.PasswordHash), []byte("at-least-12-characters")) != nil {
		t.Error("expected stored password hash to match the bootstrap password")
	}
	if acct.PasswordHash == "at-least-12-characters" {
		t.Error("password must be hashed, not stored in plaintext")
	}
}

func TestBootstrapIsNoOpWhenAdminsAlreadyExist(t *testing.T) {
	repo := newFakeRepository()
	newTestAdmin(t, repo, "existing-admin", "some-existing-password")

	err := Bootstrap(context.Background(), repo, BootstrapConfig{
		Username: "new-admin",
		Password: "at-least-12-characters",
	})
	if err != nil {
		t.Fatalf("expected bootstrap to succeed as a no-op, got: %v", err)
	}

	if _, exists := repo.adminsByUsername["new-admin"]; exists {
		t.Error("expected bootstrap not to create a new admin when one already exists")
	}
	if len(repo.adminsByID) != 1 {
		t.Errorf("expected exactly 1 admin to still exist, got %d", len(repo.adminsByID))
	}
}

func TestBootstrapFailsWhenNoAdminsExistAndNoCredentialsConfigured(t *testing.T) {
	repo := newFakeRepository()

	err := Bootstrap(context.Background(), repo, BootstrapConfig{})
	if err == nil {
		t.Fatal("expected bootstrap to fail when no admins exist and no bootstrap credentials are set")
	}
}

func TestBootstrapFailsWhenPasswordTooShort(t *testing.T) {
	repo := newFakeRepository()

	err := Bootstrap(context.Background(), repo, BootstrapConfig{
		Username: "admin",
		Password: "short",
	})
	if err == nil {
		t.Fatal("expected bootstrap to fail with a too-short password")
	}
	if _, exists := repo.adminsByUsername["admin"]; exists {
		t.Error("expected no admin to be created when the password is rejected")
	}
}

// TestBootstrapTreatsUsernameCollisionAsSuccessNotFailure guards the
// srv1/srv2/srv3-booting-together scenario: if InsertAdmin reports
// ErrAdminUsernameTaken (e.g. a concurrent peer's Bootstrap call won a
// race and inserted an admin with this exact username after this call's
// CountAdmins check but before its own InsertAdmin call), Bootstrap must
// treat that as success rather than propagating a startup failure - the
// goal ("at least one admin exists") is satisfied either way.
func TestBootstrapTreatsUsernameCollisionAsSuccessNotFailure(t *testing.T) {
	repo := newFakeRepository()
	repo.insertAdminErr = metadata.ErrAdminUsernameTaken

	err := Bootstrap(context.Background(), repo, BootstrapConfig{
		Username: "admin",
		Password: "at-least-12-characters",
	})
	if err != nil {
		t.Fatalf("expected username-collision race to be treated as success, got: %v", err)
	}
}

func TestSeedUploadSettingsCreatesRowWhenNoneExist(t *testing.T) {
	repo := newFakeRepository()

	err := SeedUploadSettings(context.Background(), repo, UploadSettingsDefaults{
		MaxUploadSizeMB:   100,
		AllowedMimeTypes:  []string{"image/*"},
		BlockedExtensions: []string{".exe"},
	})
	if err != nil {
		t.Fatalf("expected seed to succeed, got: %v", err)
	}

	settings, exists := repo.uploadSettings, repo.uploadSettings != nil
	if !exists {
		t.Fatal("expected an upload_settings row to be created")
	}
	if settings.MaxUploadSizeMB != 100 {
		t.Errorf("expected max_upload_size_mb=100, got %d", settings.MaxUploadSizeMB)
	}
	if settings.UpdatedBy != nil {
		t.Error("expected updated_by to be nil for a boot-time seed, not an admin-initiated change")
	}
}

// TestSeedUploadSettingsIsNoOpWhenRowAlreadyExists guards the same
// restart-safety property as TestBootstrapIsNoOpWhenAdminsAlreadyExist: a
// later boot with different env-var-derived defaults must not overwrite
// settings an admin has since changed via Service.UpdateUploadSettings.
func TestSeedUploadSettingsIsNoOpWhenRowAlreadyExists(t *testing.T) {
	repo := newFakeRepository()

	if err := SeedUploadSettings(context.Background(), repo, UploadSettingsDefaults{
		MaxUploadSizeMB:   100,
		AllowedMimeTypes:  []string{"image/*"},
		BlockedExtensions: []string{".exe"},
	}); err != nil {
		t.Fatalf("first seed failed: %v", err)
	}

	// Simulate an admin having since changed the settings.
	repo.uploadSettings.MaxUploadSizeMB = 999

	if err := SeedUploadSettings(context.Background(), repo, UploadSettingsDefaults{
		MaxUploadSizeMB:   100,
		AllowedMimeTypes:  []string{"image/*"},
		BlockedExtensions: []string{".exe"},
	}); err != nil {
		t.Fatalf("second seed (simulating a restart) failed: %v", err)
	}

	if repo.uploadSettings.MaxUploadSizeMB != 999 {
		t.Errorf("expected admin-changed value 999 to survive re-seed, got %d", repo.uploadSettings.MaxUploadSizeMB)
	}
}

package admin

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rizkiromadon/tempcdn/internal/idgen"
	"github.com/rizkiromadon/tempcdn/internal/metadata"
	"golang.org/x/crypto/bcrypt"
)

const bcryptCost = bcrypt.DefaultCost

type BootstrapConfig struct {
	Username string
	Password string
}

func Bootstrap(ctx context.Context, repository metadata.AdminRepository, cfg BootstrapConfig) error {
	count, err := repository.CountAdmins(ctx)
	if err != nil {
		return fmt.Errorf("count existing admins: %w", err)
	}
	if count > 0 {
		return nil
	}

	if cfg.Username == "" || cfg.Password == "" {
		return fmt.Errorf("no admin accounts exist and ADMIN_BOOTSTRAP_USERNAME/ADMIN_BOOTSTRAP_PASSWORD are not set: set both to create the first admin account, or insert one directly if you're restoring from a backup")
	}
	if len(cfg.Password) < minPasswordLength {
		return fmt.Errorf("ADMIN_BOOTSTRAP_PASSWORD must be at least %d characters", minPasswordLength)
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(cfg.Password), bcryptCost)
	if err != nil {
		return fmt.Errorf("hash bootstrap admin password: %w", err)
	}

	admin := &metadata.Admin{
		ID:           idgen.NewAdminID(),
		Username:     cfg.Username,
		PasswordHash: string(passwordHash),
		CreatedAt:    time.Now().UTC(),
	}
	if err := repository.InsertAdmin(ctx, admin); err != nil {

		if errors.Is(err, metadata.ErrAdminUsernameTaken) {
			return nil
		}
		return fmt.Errorf("insert bootstrap admin: %w", err)
	}

	return nil
}

type UploadSettingsDefaults struct {
	MaxUploadSizeMB   int64
	AllowedMimeTypes  []string
	BlockedExtensions []string
}

func SeedUploadSettings(ctx context.Context, repository metadata.UploadSettingsRepository, defaults UploadSettingsDefaults) error {
	return repository.SeedUploadSettingsIfMissing(ctx, &metadata.UploadSettings{
		MaxUploadSizeMB:   defaults.MaxUploadSizeMB,
		AllowedMimeTypes:  defaults.AllowedMimeTypes,
		BlockedExtensions: defaults.BlockedExtensions,
		UpdatedAt:         time.Now().UTC(),
	})
}

const defaultTermsContent = "No terms of service have been configured yet."

const defaultPrivacyContent = "No privacy policy has been configured yet."

// SeedLegalDocuments ensures the "terms" and "privacy" rows exist so the
// public GET endpoints never 404 on a freshly deployed instance. It's a
// placeholder until an admin sets real content via the admin API.
func SeedLegalDocuments(ctx context.Context, repository metadata.LegalDocumentRepository) error {
	now := time.Now().UTC()
	if err := repository.SeedLegalDocumentIfMissing(ctx, &metadata.LegalDocument{
		DocType:   metadata.LegalDocTerms,
		Content:   defaultTermsContent,
		UpdatedAt: now,
	}); err != nil {
		return fmt.Errorf("seed terms document: %w", err)
	}
	if err := repository.SeedLegalDocumentIfMissing(ctx, &metadata.LegalDocument{
		DocType:   metadata.LegalDocPrivacy,
		Content:   defaultPrivacyContent,
		UpdatedAt: now,
	}); err != nil {
		return fmt.Errorf("seed privacy document: %w", err)
	}
	return nil
}

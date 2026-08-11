package admin

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tempcdn/tempcdn/internal/idgen"
	"github.com/tempcdn/tempcdn/internal/metadata"
	"golang.org/x/crypto/bcrypt"
)

// bcryptCost matches bcrypt's recommended default (bcrypt.DefaultCost),
// spelled out explicitly here so a future change to the library default
// doesn't silently change this application's cost factor.
const bcryptCost = bcrypt.DefaultCost

// BootstrapConfig carries the ADMIN_BOOTSTRAP_USERNAME / _PASSWORD
// environment variables (see config.Config) needed to seed the first admin
// account.
type BootstrapConfig struct {
	Username string
	Password string
}

// Bootstrap seeds the first admin account from BootstrapConfig, but only
// if the admins table is currently empty. This makes it safe to leave
// ADMIN_BOOTSTRAP_USERNAME/PASSWORD set across restarts and redeploys:
// after the first successful boot creates the account, every later boot is
// a no-op here, and further admin accounts (if ever needed) are created
// through normal authenticated flows rather than by re-running bootstrap.
//
// Returns without error (and without creating anything) if admins already
// exist, so operators aren't forced to unset the env vars after first
// boot. Returns an error if admins is empty AND no bootstrap credentials
// were configured, since a deployment with no way to log in is a
// configuration mistake, not a valid state to silently start in.
func Bootstrap(ctx context.Context, repository metadata.Repository, cfg BootstrapConfig) error {
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
		// ErrAdminUsernameTaken here would mean another instance's
		// concurrent Bootstrap call (e.g. srv1/srv2/srv3 booting together)
		// won the race and already created an admin with this exact
		// username - not a real failure, since the goal (at least one
		// admin exists) is satisfied either way. Any *other* admin having
		// raced in with a *different* username between the CountAdmins
		// check and this insert is still fine: the goal was "at least one
		// admin exists", not "exactly the bootstrap admin exists".
		if errors.Is(err, metadata.ErrAdminUsernameTaken) {
			return nil
		}
		return fmt.Errorf("insert bootstrap admin: %w", err)
	}

	return nil
}

// UploadSettingsDefaults carries the environment-variable defaults (from
// config.Config) used to seed upload_settings on first boot - the same
// values that, before this feature, were the only source of truth for
// upload limits.
type UploadSettingsDefaults struct {
	MaxUploadSizeMB   int64
	AllowedMimeTypes  []string
	BlockedExtensions []string
}

// SeedUploadSettings ensures the single upload_settings row exists,
// inserting UploadSettingsDefaults if it doesn't. Like Bootstrap, this is a
// no-op on every boot after the first: once the row exists (whether still
// at its seeded defaults or since changed by an admin via
// Service.UpdateUploadSettings), later boots leave it untouched rather
// than reverting it back to the environment variable defaults.
func SeedUploadSettings(ctx context.Context, repository metadata.Repository, defaults UploadSettingsDefaults) error {
	return repository.SeedUploadSettingsIfMissing(ctx, &metadata.UploadSettings{
		MaxUploadSizeMB:   defaults.MaxUploadSizeMB,
		AllowedMimeTypes:  defaults.AllowedMimeTypes,
		BlockedExtensions: defaults.BlockedExtensions,
		UpdatedAt:         time.Now().UTC(),
	})
}

-- Admin accounts for the admin dashboard API. Passwords are stored only as
-- bcrypt hashes (see internal/admin.Service) - never plaintext.
CREATE TABLE IF NOT EXISTS admins (
    id            TEXT PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- last_login_at is updated on every successful login for visibility
    -- into stale/unused admin accounts; NULL means the account has never
    -- logged in (e.g. right after bootstrap seeding).
    last_login_at TIMESTAMPTZ
);

-- Opaque server-side sessions: the token handed to the client is a random
-- string, and only its SHA-256 hash is stored here (see
-- internal/admin.hashSessionToken) so that a database read/leak alone can't
-- be replayed as a valid session - the same defense-in-depth reasoning as
-- files.delete_token_hash. Sessions are rows, not JWTs, specifically so
-- they can be revoked (DELETE) on logout or forcibly invalidated without
-- waiting for expiry.
CREATE TABLE IF NOT EXISTS admin_sessions (
    token_hash TEXT PRIMARY KEY,
    admin_id   TEXT NOT NULL REFERENCES admins (id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    -- last_used_at lets an admin (or operator) see recent activity on a
    -- session and is refreshed on every authenticated request.
    last_used_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_admin_sessions_admin_id ON admin_sessions (admin_id);
CREATE INDEX IF NOT EXISTS idx_admin_sessions_expires_at ON admin_sessions (expires_at);

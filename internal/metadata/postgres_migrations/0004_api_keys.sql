-- API keys: revocable, database-backed credentials for server-to-server
-- access (e.g. Prometheus scraping /metrics), replacing the old static
-- METRICS_TOKEN environment variable. Like admin_sessions, only the
-- SHA-256 hash of the plaintext key is stored (see
-- internal/idgen.HashAPIKey) - the plaintext is shown to the admin exactly
-- once, at creation time, and never persisted or logged.
CREATE TABLE IF NOT EXISTS api_keys (
    id         TEXT PRIMARY KEY,
    -- name is an operator-supplied label (e.g. "prometheus-prod") purely
    -- for identifying keys in the admin dashboard; it has no bearing on
    -- authorization.
    name       TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- last_used_at lets an operator see whether a key is actually still in
    -- use before revoking it.
    last_used_at TIMESTAMPTZ,
    -- revoked_at marks a key as no longer valid without deleting the row,
    -- preserving an audit trail of when a key existed and when it was
    -- revoked. NULL means still active.
    revoked_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_api_keys_revoked_at ON api_keys (revoked_at);

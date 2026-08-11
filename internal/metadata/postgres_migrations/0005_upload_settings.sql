-- Runtime-configurable upload limits (max upload size, allowed MIME types,
-- blocked extensions). This replaces the old behavior of reading these
-- values only once, from SERVER_MAX_UPLOAD_MB / ALLOWED_MIME_TYPES /
-- BLOCKED_EXTENSIONS environment variables at process boot - they can now
-- be changed at runtime via the admin API (see internal/admin.Service
-- Get/UpdateUploadSettings) without a redeploy or restart.
--
-- Single-row table: id is always 1, enforced by the CHECK constraint, so
-- there is exactly one settings row per deployment (shared across every
-- instance pointed at the same database, consistent with how admins/
-- api_keys already work).
CREATE TABLE IF NOT EXISTS upload_settings (
    id                   INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    max_upload_size_mb   BIGINT NOT NULL,
    -- allowed_mime_types/blocked_extensions are stored as JSON arrays of
    -- strings (e.g. ["image/*","application/pdf"]) rather than a
    -- comma-joined TEXT column, so empty-vs-single-empty-string edge cases
    -- can't arise and the admin API can round-trip them as native JSON
    -- arrays without ad hoc split/join parsing.
    allowed_mime_types   JSONB NOT NULL DEFAULT '[]'::jsonb,
    blocked_extensions   JSONB NOT NULL DEFAULT '[]'::jsonb,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- updated_by records which admin last changed these settings, for
    -- accountability. NULL means the row was seeded from environment
    -- variable defaults at first boot and has never been changed via the
    -- admin API.
    updated_by           TEXT REFERENCES admins (id) ON DELETE SET NULL
);

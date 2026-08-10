-- Postgres equivalent of the SQLite 0001_init.sql + 0002_delete_token.sql
-- migrations, combined into one file. Unlike the SQLite runner, Migrate()
-- for Postgres tracks applied migrations in schema_migrations (see
-- postgres_repository.go), so each file here only ever runs once per
-- database - there's no need to split "add this column later" into its own
-- file or make individual statements idempotent.

CREATE TABLE IF NOT EXISTS files (
    id                 TEXT PRIMARY KEY,
    original_name      TEXT NOT NULL,
    content_type       TEXT NOT NULL,
    size_bytes         BIGINT NOT NULL,
    checksum_sha256    TEXT NOT NULL,
    object_key         TEXT NOT NULL,
    cdn_url            TEXT NOT NULL,
    uploader_ip_hash   TEXT NOT NULL,
    delete_token_hash  TEXT NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL,
    expires_at         TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_files_expires_at        ON files (expires_at);
CREATE INDEX IF NOT EXISTS idx_files_created_at        ON files (created_at);
CREATE INDEX IF NOT EXISTS idx_files_checksum_sha256   ON files (checksum_sha256);
CREATE INDEX IF NOT EXISTS idx_files_delete_token_hash ON files (delete_token_hash);

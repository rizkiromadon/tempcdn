-- Adds a per-file delete authorization token, separate from the public file
-- ID. The file ID is necessarily shared (it's embedded in the CDN URL /
-- GET endpoint), so it must not double as a delete credential. Only the
-- SHA-256 hash of the token is stored, matching how uploader IPs are
-- handled elsewhere - the plaintext token is returned to the uploader
-- exactly once, in the upload response, and never persisted or logged.
--
-- "IF NOT EXISTS" is required here (unlike a typical migration) because
-- this repository's Migrate() re-executes every file in migrations/ on
-- every process startup rather than tracking which have already been
-- applied (see internal/metadata/repository.go) - without it, this
-- statement would fail on the second startup once the column already
-- exists. Requires SQLite 3.35+ (bundled by github.com/mattn/go-sqlite3
-- v1.14.22, which ships SQLite 3.44).
ALTER TABLE files ADD COLUMN IF NOT EXISTS delete_token_hash TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_files_delete_token_hash ON files (delete_token_hash);

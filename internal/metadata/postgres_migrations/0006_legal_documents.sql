-- Admin-editable legal documents (terms of service, privacy policy).
-- Each row is a single document identified by doc_type ('terms' /
-- 'privacy'), following the same pattern as upload_settings: seeded with
-- a default on first boot, then editable at runtime via the admin API
-- (see internal/admin.Service Get/UpdateLegalDocument) without a redeploy.
CREATE TABLE IF NOT EXISTS legal_documents (
    doc_type    TEXT PRIMARY KEY CHECK (doc_type IN ('terms', 'privacy')),
    content     TEXT NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- updated_by records which admin last changed the document, for
    -- accountability. NULL means the row was seeded from the default
    -- content at first boot and has never been changed via the admin API.
    updated_by  TEXT REFERENCES admins (id) ON DELETE SET NULL
);

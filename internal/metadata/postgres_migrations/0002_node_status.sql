-- Tracks liveness of every tempcdn instance sharing this database (e.g.
-- srv1/srv2/srv3 behind a rotating frontend). Each instance UPSERTs its own
-- row on a heartbeat interval (see internal/nodestatus.Reporter); any
-- instance's background janitor (internal/nodestatus.Janitor) can then flag
-- a row as "offline" once its heartbeat goes stale, since a node cannot be
-- trusted to mark itself offline on crash/power-loss - it never gets the
-- chance to run that code.
CREATE TABLE IF NOT EXISTS node_status (
    node_id       TEXT PRIMARY KEY,
    hostname      TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'online',
    started_at    TIMESTAMPTZ NOT NULL,
    last_heartbeat_at TIMESTAMPTZ NOT NULL,
    marked_offline_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_node_status_last_heartbeat_at ON node_status (last_heartbeat_at);

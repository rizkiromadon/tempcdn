-- SQLite equivalent of postgres_migrations/0002_node_status.sql, kept for
-- Repository interface parity between SQLiteRepository and
-- PostgresRepository (see NodeStatusRepository in repository.go). Multi-node
-- heartbeat/offline-detection is only meaningful when several instances
-- share one database, which in practice means Postgres (see NewRepository),
-- but a single SQLite instance can still report its own row here so
-- GET /api/v1/nodes behaves the same regardless of backend.
CREATE TABLE IF NOT EXISTS node_status (
    node_id           TEXT PRIMARY KEY,
    hostname          TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'online',
    started_at        TIMESTAMP NOT NULL,
    last_heartbeat_at TIMESTAMP NOT NULL,
    marked_offline_at TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_node_status_last_heartbeat_at ON node_status (last_heartbeat_at);

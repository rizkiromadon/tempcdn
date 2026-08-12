package metadata

import (
	"context"
	"fmt"
	"time"
)

// This file implements NodeStatusRepository on *PostgresRepository.
// It owns everything about the `node_status` table.

func (r *PostgresRepository) Heartbeat(ctx context.Context, nodeID, hostname string, startedAt, now time.Time) error {
	const query = `
		INSERT INTO node_status (node_id, hostname, status, started_at, last_heartbeat_at, marked_offline_at)
		VALUES ($1, $2, 'online', $3, $4, NULL)
		ON CONFLICT (node_id) DO UPDATE SET
			hostname = EXCLUDED.hostname,
			status = 'online',
			last_heartbeat_at = EXCLUDED.last_heartbeat_at,
			marked_offline_at = NULL
	`
	_, err := r.pool.Exec(ctx, query, nodeID, hostname, startedAt, now)
	if err != nil {
		return fmt.Errorf("upsert node heartbeat: %w", err)
	}
	return nil
}

func (r *PostgresRepository) MarkStaleOffline(ctx context.Context, before, now time.Time) ([]string, error) {
	const query = `
		UPDATE node_status
		SET status = 'offline', marked_offline_at = $2
		WHERE status = 'online' AND last_heartbeat_at <= $1
		RETURNING node_id
	`
	rows, err := r.pool.Query(ctx, query, before, now)
	if err != nil {
		return nil, fmt.Errorf("mark stale nodes offline: %w", err)
	}
	defer rows.Close()

	var nodeIDs []string
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			return nil, fmt.Errorf("scan marked-offline node id: %w", err)
		}
		nodeIDs = append(nodeIDs, nodeID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate marked-offline node ids: %w", err)
	}
	return nodeIDs, nil
}

func (r *PostgresRepository) ListNodeStatus(ctx context.Context) ([]*NodeStatus, error) {
	const query = `
		SELECT node_id, hostname, status, started_at, last_heartbeat_at, marked_offline_at
		FROM node_status
		ORDER BY last_heartbeat_at DESC
	`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query node status: %w", err)
	}
	defer rows.Close()

	var nodes []*NodeStatus
	for rows.Next() {
		node, err := pgScanNodeStatusRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan node status row: %w", err)
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate node status rows: %w", err)
	}
	return nodes, nil
}

func pgScanNodeStatusRow(scanner pgRowScanner) (*NodeStatus, error) {
	var node NodeStatus
	var markedOfflineAt *time.Time
	err := scanner.Scan(
		&node.NodeID,
		&node.Hostname,
		&node.Status,
		&node.StartedAt,
		&node.LastHeartbeatAt,
		&markedOfflineAt,
	)
	if err != nil {
		return nil, err
	}
	node.MarkedOfflineAt = markedOfflineAt
	return &node, nil
}

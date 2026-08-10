package metadata

import "time"

type FileRecord struct {
	ID              string    `json:"id"`
	OriginalName    string    `json:"original_name"`
	ContentType     string    `json:"content_type"`
	SizeBytes       int64     `json:"size_bytes"`
	ChecksumSHA256  string    `json:"checksum_sha256"`
	ObjectKey       string    `json:"object_key"`
	CDNURL          string    `json:"cdn_url"`
	UploaderIPHash  string    `json:"-"`
	// DeleteTokenHash is the SHA-256 hash of the delete-authorization token
	// handed to the uploader once, at upload time. The plaintext token is
	// never stored. Never serialize this to any API response.
	DeleteTokenHash string    `json:"-"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
}

func (r *FileRecord) IsExpired(now time.Time) bool {
	return now.After(r.ExpiresAt)
}

// NodeStatusOnline and NodeStatusOffline are the only values stored in
// node_status.status. Online means the node's own heartbeat is recent
// enough (see Repository.MarkStaleOffline); offline means some other
// still-live node's janitor tick decided this node's heartbeat had gone
// stale and flipped it - a node never marks itself offline, since a
// crashed/powered-off node can't run that code.
const (
	NodeStatusOnline  = "online"
	NodeStatusOffline = "offline"
)

// NodeStatus is one row of node_status: the liveness record for a single
// tempcdn instance sharing this database with others (see
// Repository.Heartbeat, Repository.MarkStaleOffline, and
// Repository.ListNodeStatus).
type NodeStatus struct {
	NodeID          string     `json:"node_id"`
	Hostname        string     `json:"hostname"`
	Status          string     `json:"status"`
	StartedAt       time.Time  `json:"started_at"`
	LastHeartbeatAt time.Time  `json:"last_heartbeat_at"`
	MarkedOfflineAt *time.Time `json:"marked_offline_at,omitempty"`
}

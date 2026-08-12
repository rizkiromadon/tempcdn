package metadata

import "time"

type FileRecord struct {
	ID             string    `json:"id"`
	OriginalName   string    `json:"original_name"`
	ContentType    string    `json:"content_type"`
	SizeBytes      int64     `json:"size_bytes"`
	ChecksumSHA256 string    `json:"checksum_sha256"`
	ObjectKey      string    `json:"object_key"`
	CDNURL         string    `json:"cdn_url"`
	UploaderIPHash string    `json:"-"`

	DeleteTokenHash string    `json:"-"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
}

func (r *FileRecord) IsExpired(now time.Time) bool {
	return now.After(r.ExpiresAt)
}

const (
	NodeStatusOnline  = "online"
	NodeStatusOffline = "offline"
)

type NodeStatus struct {
	NodeID          string     `json:"node_id"`
	Hostname        string     `json:"hostname"`
	Status          string     `json:"status"`
	StartedAt       time.Time  `json:"started_at"`
	LastHeartbeatAt time.Time  `json:"last_heartbeat_at"`
	MarkedOfflineAt *time.Time `json:"marked_offline_at,omitempty"`
}

type Admin struct {
	ID           string     `json:"id"`
	Username     string     `json:"username"`
	PasswordHash string     `json:"-"`
	CreatedAt    time.Time  `json:"created_at"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
}

type AdminSession struct {
	TokenHash  string    `json:"-"`
	AdminID    string    `json:"admin_id"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	LastUsedAt time.Time `json:"last_used_at"`
}

func (s *AdminSession) IsExpired(now time.Time) bool {
	return now.After(s.ExpiresAt)
}

type APIKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	TokenHash  string     `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

func (k *APIKey) IsRevoked() bool {
	return k.RevokedAt != nil
}

type UploadSettings struct {
	MaxUploadSizeMB   int64     `json:"max_upload_size_mb"`
	AllowedMimeTypes  []string  `json:"allowed_mime_types"`
	BlockedExtensions []string  `json:"blocked_extensions"`
	UpdatedAt         time.Time `json:"updated_at"`

	UpdatedBy *string `json:"updated_by,omitempty"`
}

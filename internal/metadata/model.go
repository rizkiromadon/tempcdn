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
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}

func (r *FileRecord) IsExpired(now time.Time) bool {
	return now.After(r.ExpiresAt)
}

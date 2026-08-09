package upload

import (
	"net/http"

	"github.com/tempcdn/tempcdn/internal/response"
)

type ConfigResponse struct {
	MaxUploadSizeBytes int64    `json:"max_upload_size_bytes"`
	MaxUploadSizeMB    int64    `json:"max_upload_size_mb"`
	AllowedMimeTypes   []string `json:"allowed_mime_types"`
	BlockedExtensions  []string `json:"blocked_extensions"`
	FileTTLHours       int      `json:"file_ttl_hours"`
}

type ConfigHandler struct {
	maxUploadSizeBytes int64
	maxUploadSizeMB    int64
	allowedMimeTypes   []string
	blockedExtensions  []string
	fileTTLHours       int
}

func NewConfigHandler(maxUploadSizeMB int64, allowedMimeTypes []string, blockedExtensions []string, fileTTLHours int) *ConfigHandler {
	return &ConfigHandler{
		maxUploadSizeBytes: maxUploadSizeMB * 1024 * 1024,
		maxUploadSizeMB:    maxUploadSizeMB,
		allowedMimeTypes:   allowedMimeTypes,
		blockedExtensions:  blockedExtensions,
		fileTTLHours:       fileTTLHours,
	}
}

func (h *ConfigHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, ConfigResponse{
		MaxUploadSizeBytes: h.maxUploadSizeBytes,
		MaxUploadSizeMB:    h.maxUploadSizeMB,
		AllowedMimeTypes:   h.allowedMimeTypes,
		BlockedExtensions:  h.blockedExtensions,
		FileTTLHours:       h.fileTTLHours,
	})
}

package upload

import (
	"net/http"

	"github.com/rizkiromadon/tempcdn/internal/response"
)

type ConfigResponse struct {
	MaxUploadSizeBytes int64    `json:"max_upload_size_bytes"`
	MaxUploadSizeMB    int64    `json:"max_upload_size_mb"`
	AllowedMimeTypes   []string `json:"allowed_mime_types"`
	BlockedExtensions  []string `json:"blocked_extensions"`
	FileTTLHours       int      `json:"file_ttl_hours"`
}

type ConfigHandler struct {
	validator    *Validator
	fileTTLHours int
}

func NewConfigHandler(validator *Validator, fileTTLHours int) *ConfigHandler {
	return &ConfigHandler{
		validator:    validator,
		fileTTLHours: fileTTLHours,
	}
}

func (h *ConfigHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	maxSizeBytes, allowedMimeTypes, blockedExtensions := h.validator.Snapshot()
	response.JSON(w, http.StatusOK, ConfigResponse{
		MaxUploadSizeBytes: maxSizeBytes,
		MaxUploadSizeMB:    maxSizeBytes / (1024 * 1024),
		AllowedMimeTypes:   allowedMimeTypes,
		BlockedExtensions:  blockedExtensions,
		FileTTLHours:       h.fileTTLHours,
	})
}

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

// ConfigHandler serves the public, read-only GET /api/v1/config endpoint.
// It reads live values from the shared Validator (see Validator.Snapshot)
// rather than values fixed at startup, so this endpoint always reflects
// whatever an admin most recently set via PUT
// /api/v1/admin/upload-settings, without needing its own copy of the
// current settings or a restart to pick up a change.
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

package upload

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// ValidationError marks an error as safe to show verbatim to the client and
// genuinely the client's fault (400-class), as opposed to internal/infra
// errors from Service.Upload (temp file creation, storage puts, DB writes)
// which are 500-class and should never leak their raw text to callers.
type ValidationError struct {
	msg string
}

func (e *ValidationError) Error() string { return e.msg }

func newValidationError(format string, args ...interface{}) error {
	return &ValidationError{msg: fmt.Sprintf(format, args...)}
}

// validatorRules is the mutable part of Validator's configuration: the
// three limits an admin can change at runtime via PUT
// /api/v1/admin/upload-settings (see internal/admin.Service.
// UpdateUploadSettings). Held behind an atomic.Pointer so a request
// in-flight through Validator's methods always sees one fully-consistent
// set of rules - either entirely the old values or entirely the new ones,
// never a mix of, say, the new max size with the old MIME allowlist.
type validatorRules struct {
	maxSizeBytes      int64
	allowedMimeTypes  []string
	blockedExtensions []string
}

type Validator struct {
	rules atomic.Pointer[validatorRules]
}

func NewValidator(maxSizeBytes int64, allowedMimeTypes []string, blockedExtensions []string) *Validator {
	v := &Validator{}
	v.Update(maxSizeBytes, allowedMimeTypes, blockedExtensions)
	return v
}

// Update atomically swaps in a new set of validation rules. Safe to call
// concurrently with any of the Validate*/DetectAndValidateContentType
// methods from in-flight uploads - see validatorRules. Called whenever an
// admin changes upload settings (internal/admin.Service.
// UpdateUploadSettings), so new limits take effect immediately for the
// next upload, without a restart.
func (v *Validator) Update(maxSizeBytes int64, allowedMimeTypes []string, blockedExtensions []string) {
	// Copy the slices rather than storing the caller's backing arrays
	// directly, so a caller mutating its own slice afterward (or reusing
	// it across calls) can never retroactively change rules already
	// swapped in here.
	allowedCopy := append([]string(nil), allowedMimeTypes...)
	blockedCopy := append([]string(nil), blockedExtensions...)
	v.rules.Store(&validatorRules{
		maxSizeBytes:      maxSizeBytes,
		allowedMimeTypes:  allowedCopy,
		blockedExtensions: blockedCopy,
	})
}

// Snapshot returns the currently active rules as plain values, for
// callers (e.g. upload.ConfigHandler, upload.Handler) that need to read
// the current limits without duplicating the atomic-pointer plumbing.
func (v *Validator) Snapshot() (maxSizeBytes int64, allowedMimeTypes []string, blockedExtensions []string) {
	r := v.rules.Load()
	return r.maxSizeBytes, r.allowedMimeTypes, r.blockedExtensions
}

func (v *Validator) ValidateSize(sizeBytes int64) error {
	r := v.rules.Load()
	if sizeBytes <= 0 {
		return newValidationError("file is empty")
	}
	if sizeBytes > r.maxSizeBytes {
		return newValidationError("file exceeds maximum allowed size of %d bytes", r.maxSizeBytes)
	}
	return nil
}

func (v *Validator) ValidateExtension(originalName string) error {
	r := v.rules.Load()
	lowerName := strings.ToLower(originalName)
	extension := strings.ToLower(filepath.Ext(originalName))
	for _, blocked := range r.blockedExtensions {
		blockedLower := strings.ToLower(blocked)
		if blockedLower == extension {
			return newValidationError("file extension %s is not allowed", extension)
		}
		// Also catch blocked extensions earlier in a compound/double
		// extension (e.g. "evil.exe.png", where filepath.Ext alone would
		// only see ".png"). Content-type sniffing is still the primary
		// defense against a mislabeled file; this just avoids a
		// deceptive filename value in the meantime.
		if strings.Contains(lowerName, blockedLower+".") {
			return newValidationError("filename contains disallowed extension %s", blockedLower)
		}
	}
	return nil
}

func (v *Validator) DetectAndValidateContentType(sniffBuffer []byte) (string, error) {
	detectedType := http.DetectContentType(sniffBuffer)
	normalizedType := strings.SplitN(detectedType, ";", 2)[0]
	normalizedType = strings.TrimSpace(normalizedType)

	if v.isMimeTypeAllowed(normalizedType) {
		return normalizedType, nil
	}
	return "", newValidationError("content type %s is not allowed", normalizedType)
}

func (v *Validator) isMimeTypeAllowed(mimeType string) bool {
	r := v.rules.Load()
	for _, pattern := range r.allowedMimeTypes {
		if matchesMimePattern(pattern, mimeType) {
			return true
		}
	}
	return false
}

func matchesMimePattern(pattern string, mimeType string) bool {
	if pattern == mimeType {
		return true
	}
	if strings.HasSuffix(pattern, "/*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(mimeType, prefix)
	}
	return false
}

package upload

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
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

type Validator struct {
	maxSizeBytes      int64
	allowedMimeTypes  []string
	blockedExtensions []string
}

func NewValidator(maxSizeBytes int64, allowedMimeTypes []string, blockedExtensions []string) *Validator {
	return &Validator{
		maxSizeBytes:      maxSizeBytes,
		allowedMimeTypes:  allowedMimeTypes,
		blockedExtensions: blockedExtensions,
	}
}

func (v *Validator) ValidateSize(sizeBytes int64) error {
	if sizeBytes <= 0 {
		return newValidationError("file is empty")
	}
	if sizeBytes > v.maxSizeBytes {
		return newValidationError("file exceeds maximum allowed size of %d bytes", v.maxSizeBytes)
	}
	return nil
}

func (v *Validator) ValidateExtension(originalName string) error {
	lowerName := strings.ToLower(originalName)
	extension := strings.ToLower(filepath.Ext(originalName))
	for _, blocked := range v.blockedExtensions {
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
	for _, pattern := range v.allowedMimeTypes {
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

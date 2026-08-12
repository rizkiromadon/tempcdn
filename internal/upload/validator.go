package upload

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
)

type ValidationError struct {
	msg string
}

func (e *ValidationError) Error() string { return e.msg }

func newValidationError(format string, args ...interface{}) error {
	return &ValidationError{msg: fmt.Sprintf(format, args...)}
}

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

func (v *Validator) Update(maxSizeBytes int64, allowedMimeTypes []string, blockedExtensions []string) {

	allowedCopy := append([]string(nil), allowedMimeTypes...)
	blockedCopy := append([]string(nil), blockedExtensions...)
	v.rules.Store(&validatorRules{
		maxSizeBytes:      maxSizeBytes,
		allowedMimeTypes:  allowedCopy,
		blockedExtensions: blockedCopy,
	})
}

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

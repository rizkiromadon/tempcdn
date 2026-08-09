package upload

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
)

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
		return fmt.Errorf("file is empty")
	}
	if sizeBytes > v.maxSizeBytes {
		return fmt.Errorf("file exceeds maximum allowed size of %d bytes", v.maxSizeBytes)
	}
	return nil
}

func (v *Validator) ValidateExtension(originalName string) error {
	extension := strings.ToLower(filepath.Ext(originalName))
	for _, blocked := range v.blockedExtensions {
		if strings.ToLower(blocked) == extension {
			return fmt.Errorf("file extension %s is not allowed", extension)
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
	return "", fmt.Errorf("content type %s is not allowed", normalizedType)
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

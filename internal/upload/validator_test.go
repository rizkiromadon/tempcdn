package upload

import "testing"

func TestValidateSizeRejectsEmptyFile(t *testing.T) {
	validator := NewValidator(100, []string{"image/*"}, []string{".exe"})
	if err := validator.ValidateSize(0); err == nil {
		t.Error("expected error for empty file, got nil")
	}
}

func TestValidateSizeRejectsOversizedFile(t *testing.T) {
	validator := NewValidator(100, []string{"image/*"}, []string{".exe"})
	if err := validator.ValidateSize(101); err == nil {
		t.Error("expected error for oversized file, got nil")
	}
}

func TestValidateSizeAcceptsValidFile(t *testing.T) {
	validator := NewValidator(100, []string{"image/*"}, []string{".exe"})
	if err := validator.ValidateSize(50); err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestValidateExtensionRejectsBlockedExtension(t *testing.T) {
	validator := NewValidator(100, []string{"image/*"}, []string{".exe", ".bat"})
	if err := validator.ValidateExtension("malware.EXE"); err == nil {
		t.Error("expected error for blocked extension, got nil")
	}
}

func TestValidateExtensionAcceptsAllowedExtension(t *testing.T) {
	validator := NewValidator(100, []string{"image/*"}, []string{".exe", ".bat"})
	if err := validator.ValidateExtension("photo.png"); err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestDetectAndValidateContentTypeAcceptsWildcardMatch(t *testing.T) {
	validator := NewValidator(100, []string{"image/*"}, nil)
	pngHeader := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	detected, err := validator.DetectAndValidateContentType(pngHeader)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if detected != "image/png" {
		t.Errorf("expected image/png, got %s", detected)
	}
}

func TestDetectAndValidateContentTypeRejectsDisallowedType(t *testing.T) {
	validator := NewValidator(100, []string{"image/*"}, nil)
	plainTextBytes := []byte("MZ this is actually an executable disguised as text")
	_, err := validator.DetectAndValidateContentType(plainTextBytes)
	if err == nil {
		t.Error("expected error for disallowed content type, got nil")
	}
}

func TestDetectAndValidateContentTypeAcceptsExactMatch(t *testing.T) {
	validator := NewValidator(100, []string{"application/pdf"}, nil)
	pdfHeader := []byte("%PDF-1.4 rest of pdf content here padding padding")
	detected, err := validator.DetectAndValidateContentType(pdfHeader)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if detected != "application/pdf" {
		t.Errorf("expected application/pdf, got %s", detected)
	}
}

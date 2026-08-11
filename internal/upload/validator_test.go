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

// TestUpdateTakesEffectImmediately guards the core promise behind runtime-
// configurable upload settings (see admin.Handler.UpdateUploadSettings and
// main.go's SetUploadSettingsUpdatedCallback wiring): a call to Update
// must be visible to the very next validation call on the same Validator,
// with no restart or additional synchronization required by the caller.
func TestUpdateTakesEffectImmediately(t *testing.T) {
	validator := NewValidator(100, []string{"image/*"}, []string{".exe"})

	if err := validator.ValidateSize(150); err == nil {
		t.Fatal("expected 150 to exceed the initial 100-byte limit")
	}

	validator.Update(200, []string{"image/*"}, []string{".exe"})

	if err := validator.ValidateSize(150); err != nil {
		t.Errorf("expected 150 to be within the updated 200-byte limit, got error: %v", err)
	}
}

// TestUpdateReplacesRulesAtomically guards against a caller-visible
// half-updated state: every field read back via Snapshot after Update must
// come from the same Update call, never a mix of an old and new value.
func TestUpdateReplacesRulesAtomically(t *testing.T) {
	validator := NewValidator(100, []string{"image/*"}, []string{".exe"})

	validator.Update(300, []string{"application/pdf"}, []string{".bat", ".sh"})

	maxSize, allowed, blocked := validator.Snapshot()
	if maxSize != 300 {
		t.Errorf("expected updated max size 300, got %d", maxSize)
	}
	if len(allowed) != 1 || allowed[0] != "application/pdf" {
		t.Errorf("expected updated allowed mime types [application/pdf], got %v", allowed)
	}
	if len(blocked) != 2 || blocked[0] != ".bat" || blocked[1] != ".sh" {
		t.Errorf("expected updated blocked extensions [.bat .sh], got %v", blocked)
	}
}

// TestUpdateDoesNotAliasCallerSlice guards against a caller's slice being
// captured by reference: mutating the slice passed to Update after the
// call must not retroactively change the rules already stored.
func TestUpdateDoesNotAliasCallerSlice(t *testing.T) {
	validator := NewValidator(100, []string{"image/*"}, []string{".exe"})

	allowed := []string{"application/pdf"}
	validator.Update(100, allowed, nil)
	allowed[0] = "image/*" // mutate after the call

	_, snapshotAllowed, _ := validator.Snapshot()
	if snapshotAllowed[0] != "application/pdf" {
		t.Errorf("expected Update to have copied the slice, but mutating the caller's slice changed the stored rules: got %v", snapshotAllowed)
	}
}

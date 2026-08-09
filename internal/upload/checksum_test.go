package upload

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"
)

func TestChecksummingReaderProducesCorrectHashWhileStreaming(t *testing.T) {
	content := []byte("the quick brown fox jumps over the lazy dog")
	expectedSum := sha256.Sum256(content)
	expectedHex := hex.EncodeToString(expectedSum[:])

	reader := NewChecksummingReader(bytes.NewReader(content))

	readContent, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("unexpected error reading: %v", err)
	}
	if !bytes.Equal(readContent, content) {
		t.Error("content read through ChecksummingReader does not match original")
	}

	if reader.SumHex() != expectedHex {
		t.Errorf("expected checksum %s, got %s", expectedHex, reader.SumHex())
	}
}

func TestChecksummingReaderIdenticalContentProducesIdenticalHash(t *testing.T) {
	contentA := []byte("duplicate file content")
	contentB := []byte("duplicate file content")

	readerA := NewChecksummingReader(bytes.NewReader(contentA))
	_, _ = io.ReadAll(readerA)

	readerB := NewChecksummingReader(bytes.NewReader(contentB))
	_, _ = io.ReadAll(readerB)

	if readerA.SumHex() != readerB.SumHex() {
		t.Error("expected identical content to produce identical checksums")
	}
}

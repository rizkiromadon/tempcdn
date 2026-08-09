package upload

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
)

type ChecksummingReader struct {
	teeReader io.Reader
	hasher    hash.Hash
}

func NewChecksummingReader(source io.Reader) *ChecksummingReader {
	hasher := sha256.New()
	return &ChecksummingReader{
		teeReader: io.TeeReader(source, hasher),
		hasher:    hasher,
	}
}

func (c *ChecksummingReader) Read(buffer []byte) (int, error) {
	return c.teeReader.Read(buffer)
}

func (c *ChecksummingReader) SumHex() string {
	return hex.EncodeToString(c.hasher.Sum(nil))
}

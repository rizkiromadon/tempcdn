package idgen

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
)

func NewFileID() string {
	return uuid.New().String()
}

func NewAdminID() string {
	return uuid.New().String()
}

// NewSessionToken returns a high-entropy, URL-safe secret for an admin
// session, handed to the client once at login time (see
// internal/admin.Service.Login). Same reasoning as NewDeleteToken.
func NewSessionToken() (string, error) {
	buf := make([]byte, 32) // 256 bits
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random session token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// HashSessionToken returns the SHA-256 hex digest of a session token, for
// storage/comparison. Same reasoning as HashDeleteToken: the token already
// has 256 bits of CSPRNG entropy, so a fast hash is sufficient here (unlike
// password hashing, which needs a slow KDF - see internal/admin for that).
func HashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// SessionTokenMatches compares a candidate session token against the
// stored hash in constant time, to avoid leaking timing information.
func SessionTokenMatches(candidateToken string, storedHash string) bool {
	if candidateToken == "" || storedHash == "" {
		return false
	}
	candidateHash := HashSessionToken(candidateToken)
	return subtle.ConstantTimeCompare([]byte(candidateHash), []byte(storedHash)) == 1
}

// NewDeleteToken returns a high-entropy, URL-safe secret distinct from the
// file ID. It is handed to the uploader once and required to authorize
// deletion, so that knowing a file's public ID (necessarily shared, since
// it's embedded in the CDN URL) is not sufficient to delete it.
func NewDeleteToken() (string, error) {
	buf := make([]byte, 32) // 256 bits
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random delete token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// HashDeleteToken returns the SHA-256 hex digest of a delete token, for
// storage/comparison. The token itself already has 256 bits of entropy from
// a CSPRNG, so unlike password hashing this doesn't need a slow KDF or a
// per-record salt to resist brute force or rainbow tables.
func HashDeleteToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// DeleteTokenMatches compares a candidate token against the stored hash in
// constant time, to avoid leaking timing information about the hash.
func DeleteTokenMatches(candidateToken string, storedHash string) bool {
	if candidateToken == "" || storedHash == "" {
		return false
	}
	candidateHash := HashDeleteToken(candidateToken)
	return subtle.ConstantTimeCompare([]byte(candidateHash), []byte(storedHash)) == 1
}

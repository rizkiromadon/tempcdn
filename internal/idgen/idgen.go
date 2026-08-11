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

// apiKeyPrefix is prepended to every plaintext API key so that keys are
// visually distinguishable from admin session tokens or delete tokens at a
// glance (e.g. in logs, scrape configs, or accidental commits) and so
// automated secret scanners have a stable pattern to key off of.
const apiKeyPrefix = "tcdn_"

// NewAPIKey returns a high-entropy, prefixed secret for server-to-server
// authentication (e.g. Prometheus scraping /metrics), handed to the admin
// once at creation time (see internal/admin.Service.CreateAPIKey). Same
// entropy and one-time-display reasoning as NewSessionToken.
func NewAPIKey() (string, error) {
	buf := make([]byte, 32) // 256 bits
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random api key: %w", err)
	}
	return apiKeyPrefix + hex.EncodeToString(buf), nil
}

// HashAPIKey returns the SHA-256 hex digest of an API key, for
// storage/comparison. Same reasoning as HashSessionToken: the key already
// has 256 bits of CSPRNG entropy, so a fast hash is sufficient.
func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// APIKeyMatches compares a candidate API key against the stored hash in
// constant time, to avoid leaking timing information.
func APIKeyMatches(candidateKey string, storedHash string) bool {
	if candidateKey == "" || storedHash == "" {
		return false
	}
	candidateHash := HashAPIKey(candidateKey)
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

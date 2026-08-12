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

func NewSessionToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random session token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func HashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func SessionTokenMatches(candidateToken string, storedHash string) bool {
	if candidateToken == "" || storedHash == "" {
		return false
	}
	candidateHash := HashSessionToken(candidateToken)
	return subtle.ConstantTimeCompare([]byte(candidateHash), []byte(storedHash)) == 1
}

const apiKeyPrefix = "tcdn_"

func NewAPIKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random api key: %w", err)
	}
	return apiKeyPrefix + hex.EncodeToString(buf), nil
}

func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func APIKeyMatches(candidateKey string, storedHash string) bool {
	if candidateKey == "" || storedHash == "" {
		return false
	}
	candidateHash := HashAPIKey(candidateKey)
	return subtle.ConstantTimeCompare([]byte(candidateHash), []byte(storedHash)) == 1
}

func NewDeleteToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random delete token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func HashDeleteToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func DeleteTokenMatches(candidateToken string, storedHash string) bool {
	if candidateToken == "" || storedHash == "" {
		return false
	}
	candidateHash := HashDeleteToken(candidateToken)
	return subtle.ConstantTimeCompare([]byte(candidateHash), []byte(storedHash)) == 1
}

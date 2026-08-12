package nodestatus

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

func ResolveNodeID(configured, hostname string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	suffix, err := randomHex(4)
	if err != nil {
		return "", fmt.Errorf("generate random node id suffix: %w", err)
	}
	if hostname == "" {
		hostname = "node"
	}
	return hostname + "-" + suffix, nil
}

func randomHex(byteLen int) (string, error) {
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

package nodestatus

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// ResolveNodeID returns configured (from NODE_ID) if non-empty, otherwise
// derives a random-suffixed ID from hostname so two instances that happen
// to share a hostname (e.g. same container image, no NODE_ID set) still get
// distinct rows in node_status instead of overwriting each other's
// heartbeat. Operators running more than one instance are expected to set
// NODE_ID explicitly (e.g. "srv1", "srv2") for stable, human-readable rows
// across restarts; the random fallback exists so the feature degrades
// gracefully rather than silently merging nodes when that's forgotten.
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

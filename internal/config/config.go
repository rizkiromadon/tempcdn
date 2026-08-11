package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// insecureDefaultIPHashSalt is used only when IP_HASH_SALT is unset. It is
// intentionally recognizable so validate() can detect and warn/fail when an
// operator forgot to set a real secret, since a known salt makes hashed IPs
// trivially reversible via a rainbow table of common IPs.
const insecureDefaultIPHashSalt = "insecure-default-salt"

type Config struct {
	ServerPort        string
	ServerMaxUploadMB int64

	R2AccountID       string
	R2AccessKeyID     string
	R2SecretAccessKey string
	R2BucketName      string
	R2Endpoint        string
	R2PublicBaseURL   string

	DatabaseDSN           string
	DatabaseMaxConns      int32
	FileTTLHours          int
	FileSweepIntervalMins int

	// NodeID identifies this process's own row in node_status when running
	// more than one instance against a shared database (see
	// nodestatus.ResolveNodeID for the fallback when unset).
	NodeID string
	// NodeHeartbeatIntervalSecs is how often this instance upserts its own
	// node_status row.
	NodeHeartbeatIntervalSecs int
	// NodeStaleAfterSecs is how long a node's last_heartbeat_at can go
	// without an update before another instance's janitor flags it
	// offline. Must be comfortably larger than
	// NodeHeartbeatIntervalSecs so a single missed heartbeat (e.g. one
	// slow DB write) doesn't flip a healthy node offline.
	NodeStaleAfterSecs int
	// NodeJanitorIntervalSecs is how often this instance checks every
	// node's row for staleness.
	NodeJanitorIntervalSecs int

	RateLimitMaxConcurrentUploads int

	IPHashSalt string

	AllowedOrigin string

	MetricsToken string

	AllowedMimeTypes  []string
	BlockedExtensions []string

	CloudflareCacheEnabled bool
	CloudflareZoneID       string
	CloudflareAPIToken     string
}

func Load() (*Config, error) {
	cfg := &Config{
		ServerPort:        getEnvOrDefault("SERVER_PORT", "8080"),
		DatabaseDSN:       getEnvOrDefault("DATABASE_DSN", "file:tempcdn.db?cache=shared&_fk=1&_journal_mode=WAL&_busy_timeout=5000"),
		R2AccountID:       os.Getenv("R2_ACCOUNT_ID"),
		R2AccessKeyID:     os.Getenv("R2_ACCESS_KEY_ID"),
		R2SecretAccessKey: os.Getenv("R2_SECRET_ACCESS_KEY"),
		R2BucketName:      getEnvOrDefault("R2_BUCKET_NAME", "tempcdn-files"),
		R2Endpoint:        os.Getenv("R2_ENDPOINT"),
		R2PublicBaseURL:   os.Getenv("R2_PUBLIC_BASE_URL"),
		IPHashSalt:        getEnvOrDefault("IP_HASH_SALT", insecureDefaultIPHashSalt),
		AllowedOrigin:     os.Getenv("ALLOWED_ORIGIN"),
		MetricsToken:      os.Getenv("METRICS_TOKEN"),
		NodeID:            os.Getenv("NODE_ID"),

		CloudflareZoneID:   os.Getenv("CLOUDFLARE_ZONE_ID"),
		CloudflareAPIToken: os.Getenv("CLOUDFLARE_API_TOKEN"),
	}

	cloudflareEnabled, err := parseBoolOrDefault("CLOUDFLARE_CACHE_ENABLED", false)
	if err != nil {
		return nil, err
	}
	cfg.CloudflareCacheEnabled = cloudflareEnabled

	maxUploadMB, err := parseIntOrDefault("SERVER_MAX_UPLOAD_MB", 100)
	if err != nil {
		return nil, err
	}
	cfg.ServerMaxUploadMB = int64(maxUploadMB)

	// DATABASE_MAX_CONNS caps the Postgres connection pool per instance.
	// Kept small by default (5) since managed Postgres providers (e.g.
	// Aiven's smaller tiers) often reserve only a handful of non-superuser
	// connection slots; exceeding them surfaces as "remaining connection
	// slots are reserved for roles with the SUPERUSER attribute" on every
	// subsequent connection attempt. Raise this only if the database's
	// connection limit and this application's expected concurrency justify
	// it - remember multi-instance deployments (see docker-compose.multi.yml)
	// multiply this value by the number of instances sharing the database.
	maxDBConns, err := parseIntOrDefault("DATABASE_MAX_CONNS", 5)
	if err != nil {
		return nil, err
	}
	cfg.DatabaseMaxConns = int32(maxDBConns)

	ttlHours, err := parseIntOrDefault("FILE_TTL_HOURS", 24)
	if err != nil {
		return nil, err
	}
	cfg.FileTTLHours = ttlHours

	sweepIntervalMins, err := parseIntOrDefault("FILE_SWEEP_INTERVAL_MINUTES", 5)
	if err != nil {
		return nil, err
	}
	cfg.FileSweepIntervalMins = sweepIntervalMins

	// SERVER_MAX_CONCURRENT_UPLOADS is the current name for this setting: it's
	// a global, process-wide concurrency cap, not a per-client rate limit, so
	// it intentionally doesn't live in the RATE_LIMIT_* namespace (see L5 in
	// the code review - that naming invited confusion with per-IP limiting,
	// which this application does not implement). The old
	// RATE_LIMIT_MAX_CONCURRENT_UPLOADS name is still honored for one
	// deprecation cycle so existing deployments don't silently fall back to
	// the default.
	maxConcurrent, err := parseIntOrDefault("SERVER_MAX_CONCURRENT_UPLOADS", -1)
	if err != nil {
		return nil, err
	}
	if maxConcurrent == -1 {
		legacyMaxConcurrent, err := parseIntOrDefault("RATE_LIMIT_MAX_CONCURRENT_UPLOADS", 50)
		if err != nil {
			return nil, err
		}
		maxConcurrent = legacyMaxConcurrent
	}
	cfg.RateLimitMaxConcurrentUploads = maxConcurrent

	heartbeatIntervalSecs, err := parseIntOrDefault("NODE_HEARTBEAT_INTERVAL_SECONDS", 15)
	if err != nil {
		return nil, err
	}
	cfg.NodeHeartbeatIntervalSecs = heartbeatIntervalSecs

	staleAfterSecs, err := parseIntOrDefault("NODE_STALE_AFTER_SECONDS", 45)
	if err != nil {
		return nil, err
	}
	cfg.NodeStaleAfterSecs = staleAfterSecs

	janitorIntervalSecs, err := parseIntOrDefault("NODE_JANITOR_INTERVAL_SECONDS", 20)
	if err != nil {
		return nil, err
	}
	cfg.NodeJanitorIntervalSecs = janitorIntervalSecs

	cfg.AllowedMimeTypes = splitAndTrim(getEnvOrDefault("ALLOWED_MIME_TYPES", "image/*,video/*,application/pdf,application/zip,text/plain"))
	cfg.BlockedExtensions = splitAndTrim(getEnvOrDefault("BLOCKED_EXTENSIONS", ".exe,.bat,.sh,.msi,.dll,.scr"))

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) validate() error {
	if c.R2Endpoint == "" {
		return fmt.Errorf("R2_ENDPOINT must be set")
	}
	if c.R2AccessKeyID == "" || c.R2SecretAccessKey == "" {
		return fmt.Errorf("R2_ACCESS_KEY_ID and R2_SECRET_ACCESS_KEY must be set")
	}
	if c.R2PublicBaseURL == "" {
		return fmt.Errorf("R2_PUBLIC_BASE_URL must be set")
	}
	if c.R2BucketName == "" {
		return fmt.Errorf("R2_BUCKET_NAME must be set")
	}
	if c.AllowedOrigin == "" {
		return fmt.Errorf("ALLOWED_ORIGIN must be set")
	}
	if c.IPHashSalt == insecureDefaultIPHashSalt || c.IPHashSalt == "" {
		allowInsecure, err := parseBoolOrDefault("ALLOW_INSECURE_IP_HASH_SALT", false)
		if err != nil {
			return err
		}
		if !allowInsecure {
			return fmt.Errorf("IP_HASH_SALT must be set to a random secret (refusing to start with the well-known default; set ALLOW_INSECURE_IP_HASH_SALT=true to override for local development only)")
		}
	}
	if c.DatabaseMaxConns <= 0 {
		return fmt.Errorf("DATABASE_MAX_CONNS must be positive")
	}
	if c.NodeHeartbeatIntervalSecs <= 0 {
		return fmt.Errorf("NODE_HEARTBEAT_INTERVAL_SECONDS must be positive")
	}
	if c.NodeStaleAfterSecs <= c.NodeHeartbeatIntervalSecs {
		return fmt.Errorf("NODE_STALE_AFTER_SECONDS (%d) must be greater than NODE_HEARTBEAT_INTERVAL_SECONDS (%d), or a healthy node with one slow heartbeat would be flagged offline", c.NodeStaleAfterSecs, c.NodeHeartbeatIntervalSecs)
	}
	if c.NodeJanitorIntervalSecs <= 0 {
		return fmt.Errorf("NODE_JANITOR_INTERVAL_SECONDS must be positive")
	}
	if c.CloudflareCacheEnabled {
		if c.CloudflareZoneID == "" {
			return fmt.Errorf("CLOUDFLARE_ZONE_ID must be set when CLOUDFLARE_CACHE_ENABLED is true")
		}
		if c.CloudflareAPIToken == "" {
			return fmt.Errorf("CLOUDFLARE_API_TOKEN must be set when CLOUDFLARE_CACHE_ENABLED is true")
		}
	}
	return nil
}

func getEnvOrDefault(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func parseIntOrDefault(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid integer value for %s: %w", key, err)
	}
	return parsed, nil
}

func parseBoolOrDefault(key string, fallback bool) (bool, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("invalid boolean value for %s: %w", key, err)
	}
	return parsed, nil
}

func splitAndTrim(raw string) []string {
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

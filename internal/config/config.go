package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	ServerPort        string
	ServerMaxUploadMB int64

	R2AccountID       string
	R2AccessKeyID     string
	R2SecretAccessKey string
	R2BucketName      string
	R2Endpoint        string
	R2PublicBaseURL   string

	DatabaseDSN  string
	FileTTLHours int

	RateLimitMaxConcurrentUploads int

	IPHashSalt string

	AllowedMimeTypes  []string
	BlockedExtensions []string
}

func Load() (*Config, error) {
	cfg := &Config{
		ServerPort:        getEnvOrDefault("SERVER_PORT", "8080"),
		DatabaseDSN:       getEnvOrDefault("DATABASE_DSN", "file:tempcdn.db?cache=shared&_fk=1"),
		R2AccountID:       os.Getenv("R2_ACCOUNT_ID"),
		R2AccessKeyID:     os.Getenv("R2_ACCESS_KEY_ID"),
		R2SecretAccessKey: os.Getenv("R2_SECRET_ACCESS_KEY"),
		R2BucketName:      getEnvOrDefault("R2_BUCKET_NAME", "tempcdn-files"),
		R2Endpoint:        os.Getenv("R2_ENDPOINT"),
		R2PublicBaseURL:   os.Getenv("R2_PUBLIC_BASE_URL"),
		IPHashSalt:        getEnvOrDefault("IP_HASH_SALT", "insecure-default-salt"),
	}

	maxUploadMB, err := parseIntOrDefault("SERVER_MAX_UPLOAD_MB", 100)
	if err != nil {
		return nil, err
	}
	cfg.ServerMaxUploadMB = int64(maxUploadMB)

	ttlHours, err := parseIntOrDefault("FILE_TTL_HOURS", 24)
	if err != nil {
		return nil, err
	}
	cfg.FileTTLHours = ttlHours

	maxConcurrent, err := parseIntOrDefault("RATE_LIMIT_MAX_CONCURRENT_UPLOADS", 50)
	if err != nil {
		return nil, err
	}
	cfg.RateLimitMaxConcurrentUploads = maxConcurrent

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

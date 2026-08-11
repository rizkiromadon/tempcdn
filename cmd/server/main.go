package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tempcdn/tempcdn/internal/admin"
	"github.com/tempcdn/tempcdn/internal/cloudflare"
	"github.com/tempcdn/tempcdn/internal/config"
	"github.com/tempcdn/tempcdn/internal/file"
	"github.com/tempcdn/tempcdn/internal/httpserver"
	"github.com/tempcdn/tempcdn/internal/logger"
	"github.com/tempcdn/tempcdn/internal/metadata"
	"github.com/tempcdn/tempcdn/internal/nodestatus"
	"github.com/tempcdn/tempcdn/internal/ratelimit"
	"github.com/tempcdn/tempcdn/internal/stats"
	"github.com/tempcdn/tempcdn/internal/storage"
	"github.com/tempcdn/tempcdn/internal/sweeper"
	"github.com/tempcdn/tempcdn/internal/upload"
)

func main() {
	log := logger.New()

	cfg, err := config.Load()
	if err != nil {
		log.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	// rootCtx is cancelled on shutdown signal so the background sweeper
	// stops cleanly alongside the HTTP server.
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()

	// DATABASE_DSN must be a "postgres://" or "postgresql://" connection
	// string: Postgres is required for every deployment, including a
	// single standalone instance, not just multi-instance setups (e.g.
	// srv1/srv2/srv3 behind a rotating frontend).
	repository, err := metadata.NewRepository(rootCtx, cfg.DatabaseDSN, cfg.DatabaseMaxConns)
	if err != nil {
		log.Error("failed to initialize metadata repository", "error", err)
		os.Exit(1)
	}
	defer repository.Close()

	if err := repository.Migrate(rootCtx); err != nil {
		log.Error("failed to run metadata migrations", "error", err)
		os.Exit(1)
	}

	// Seeds the first admin account if none exists yet. No-op on every boot
	// after the first, so ADMIN_BOOTSTRAP_USERNAME/PASSWORD are safe to
	// leave set across restarts/redeploys.
	if err := admin.Bootstrap(rootCtx, repository, admin.BootstrapConfig{
		Username: cfg.AdminBootstrapUsername,
		Password: cfg.AdminBootstrapPassword,
	}); err != nil {
		log.Error("failed to bootstrap admin account", "error", err)
		os.Exit(1)
	}

	adminService := admin.NewService(repository)
	adminHandler := admin.NewHandler(adminService, log)

	adminSessionJanitor := admin.NewSessionJanitor(repository, log)
	go adminSessionJanitor.Run(rootCtx)

	// Seeds the initial upload_settings row from SERVER_MAX_UPLOAD_MB /
	// ALLOWED_MIME_TYPES / BLOCKED_EXTENSIONS if it doesn't already exist -
	// no-op on every boot after the first, so an admin's later changes via
	// PUT /api/v1/admin/upload-settings survive restarts/redeploys instead
	// of being overwritten back to these env var defaults.
	if err := admin.SeedUploadSettings(rootCtx, repository, admin.UploadSettingsDefaults{
		MaxUploadSizeMB:   cfg.ServerMaxUploadMB,
		AllowedMimeTypes:  cfg.AllowedMimeTypes,
		BlockedExtensions: cfg.BlockedExtensions,
	}); err != nil {
		log.Error("failed to seed upload settings", "error", err)
		os.Exit(1)
	}

	uploadSettings, err := repository.GetUploadSettings(rootCtx)
	if err != nil {
		log.Error("failed to load upload settings", "error", err)
		os.Exit(1)
	}

	objectStorage, err := storage.NewR2Client(rootCtx, storage.R2ClientConfig{
		AccessKeyID:     cfg.R2AccessKeyID,
		SecretAccessKey: cfg.R2SecretAccessKey,
		BucketName:      cfg.R2BucketName,
		Endpoint:        cfg.R2Endpoint,
	})
	if err != nil {
		log.Error("failed to initialize r2 storage client", "error", err)
		os.Exit(1)
	}

	metrics := httpserver.NewMetrics()

	// validator is seeded from the database (not directly from cfg), so
	// that a value an admin previously set via PUT
	// /api/v1/admin/upload-settings survives this restart. Its rules can
	// change again at runtime after boot - see the
	// SetUploadSettingsUpdatedCallback wiring below - without needing
	// another restart.
	validator := upload.NewValidator(
		uploadSettings.MaxUploadSizeMB*1024*1024,
		uploadSettings.AllowedMimeTypes,
		uploadSettings.BlockedExtensions,
	)

	// Whenever an admin successfully changes upload settings through the
	// API, push the new values into this instance's in-memory validator
	// immediately. Note this only updates the instance that served the
	// request - see the README/deployment notes for multi-instance
	// (srv1/srv2/srv3) deployments on how other instances pick up the
	// change (each instance re-reads upload_settings from the database on
	// its own restart; near-term cross-instance propagation without a
	// restart is not yet implemented).
	adminHandler.SetUploadSettingsUpdatedCallback(func(settings *metadata.UploadSettings) {
		validator.Update(settings.MaxUploadSizeMB*1024*1024, settings.AllowedMimeTypes, settings.BlockedExtensions)
	})

	uploadService := upload.NewService(
		repository,
		objectStorage,
		validator,
		time.Duration(cfg.FileTTLHours)*time.Hour,
		cfg.R2PublicBaseURL,
	)

	concurrencyLimiter := ratelimit.NewConcurrencyLimiter(cfg.RateLimitMaxConcurrentUploads)

	uploadHandler := upload.NewHandler(
		uploadService,
		validator,
		concurrencyLimiter,
		cfg.IPHashSalt,
		metrics.UploadsTotal,
		metrics.UploadBytesTotal,
		metrics.UploadErrorsTotal,
		log,
	)

	var cachePurger cloudflare.Purger
	if cfg.CloudflareCacheEnabled {
		cachePurger = cloudflare.NewClient(cfg.CloudflareZoneID, cfg.CloudflareAPIToken)
	}

	fileService := file.NewService(repository, objectStorage, cachePurger, cfg.CloudflareCacheEnabled, log)
	fileHandler := file.NewHandler(fileService)

	configHandler := upload.NewConfigHandler(validator, cfg.FileTTLHours)

	statsHandler := stats.NewHandler(
		repository,
		metrics.UploadsTotal,
		metrics.UploadBytesTotal,
		metrics.UploadErrorsTotal,
	)

	// Node liveness: lets every instance sharing DATABASE_DSN (e.g.
	// srv1/srv2/srv3 behind a rotating frontend) push its own heartbeat and
	// have any still-live instance flag a node offline once its heartbeat
	// goes stale. A single standalone instance still reports its own row,
	// it just never sees any peers.
	hostname, err := os.Hostname()
	if err != nil {
		log.Error("failed to read hostname", "error", err)
		os.Exit(1)
	}
	nodeID, err := nodestatus.ResolveNodeID(cfg.NodeID, hostname)
	if err != nil {
		log.Error("failed to resolve node id", "error", err)
		os.Exit(1)
	}
	log.Info("nodestatus_starting", "node_id", nodeID, "hostname", hostname)

	nodeReporter := nodestatus.NewReporter(
		repository,
		nodeID,
		hostname,
		time.Duration(cfg.NodeHeartbeatIntervalSecs)*time.Second,
		log,
	)
	go nodeReporter.Run(rootCtx)

	nodeJanitor := nodestatus.NewJanitor(
		repository,
		time.Duration(cfg.NodeJanitorIntervalSecs)*time.Second,
		time.Duration(cfg.NodeStaleAfterSecs)*time.Second,
		log,
	)
	go nodeJanitor.Run(rootCtx)

	nodeStatusHandler := nodestatus.NewHandler(repository)

	router := httpserver.NewRouter(httpserver.RouterDependencies{
		Logger:            log,
		UploadHandler:     uploadHandler,
		FileHandler:       fileHandler,
		ConfigHandler:     configHandler,
		StatsHandler:      statsHandler,
		NodeStatusHandler: nodeStatusHandler,
		AdminHandler:      adminHandler,
		AdminService:      adminService,
		AllowedOrigin:     cfg.AllowedOrigin,
		RequestLatency:    metrics.RequestLatency,
	})

	// Expiry sweeper: this is the primary, application-level enforcement of
	// file expiry. Any R2 Lifecycle Rule configured on the bucket is
	// defense-in-depth on top of this, not a substitute for it.
	expirySweeper := sweeper.New(
		repository,
		objectStorage,
		cachePurger,
		cfg.CloudflareCacheEnabled,
		time.Duration(cfg.FileSweepIntervalMins)*time.Minute,
		log,
	)
	go expirySweeper.Run(rootCtx)

	server := &http.Server{
		Addr:         ":" + cfg.ServerPort,
		Handler:      router,
		ReadTimeout:  5 * time.Minute,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  90 * time.Second,
	}

	go func() {
		log.Info("server_starting", "port", cfg.ServerPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("server_failed", "error", err)
			os.Exit(1)
		}
	}()

	shutdownSignal := make(chan os.Signal, 1)
	signal.Notify(shutdownSignal, syscall.SIGINT, syscall.SIGTERM)
	<-shutdownSignal

	log.Info("server_shutting_down")

	cancelRoot()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error("server_shutdown_error", "error", err)
	}
}

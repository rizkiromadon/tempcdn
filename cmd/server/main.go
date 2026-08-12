package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rizkiromadon/tempcdn/internal/admin"
	"github.com/rizkiromadon/tempcdn/internal/cloudflare"
	"github.com/rizkiromadon/tempcdn/internal/config"
	"github.com/rizkiromadon/tempcdn/internal/file"
	"github.com/rizkiromadon/tempcdn/internal/httpserver"
	"github.com/rizkiromadon/tempcdn/internal/logger"
	"github.com/rizkiromadon/tempcdn/internal/metadata"
	"github.com/rizkiromadon/tempcdn/internal/nodestatus"
	"github.com/rizkiromadon/tempcdn/internal/ratelimit"
	"github.com/rizkiromadon/tempcdn/internal/stats"
	"github.com/rizkiromadon/tempcdn/internal/storage"
	"github.com/rizkiromadon/tempcdn/internal/sweeper"
	"github.com/rizkiromadon/tempcdn/internal/upload"
)

func main() {
	log := logger.New()

	cfg, err := config.Load()
	if err != nil {
		log.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	rootCtx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()

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

	validator := upload.NewValidator(
		uploadSettings.MaxUploadSizeMB*1024*1024,
		uploadSettings.AllowedMimeTypes,
		uploadSettings.BlockedExtensions,
	)

	adminHandler.SetUploadSettingsUpdatedCallback(func(settings *metadata.UploadSettings) {
		validator.Update(settings.MaxUploadSizeMB*1024*1024, settings.AllowedMimeTypes, settings.BlockedExtensions)
	})

	settingsSynchronizer := upload.NewSettingsSynchronizer(repository, validator, log)
	go settingsSynchronizer.Run(rootCtx)

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

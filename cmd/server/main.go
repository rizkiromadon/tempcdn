package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tempcdn/tempcdn/internal/cloudflare"
	"github.com/tempcdn/tempcdn/internal/config"
	"github.com/tempcdn/tempcdn/internal/file"
	"github.com/tempcdn/tempcdn/internal/httpserver"
	"github.com/tempcdn/tempcdn/internal/logger"
	"github.com/tempcdn/tempcdn/internal/metadata"
	"github.com/tempcdn/tempcdn/internal/ratelimit"
	"github.com/tempcdn/tempcdn/internal/storage"
	"github.com/tempcdn/tempcdn/internal/upload"
)

func main() {
	log := logger.New()

	cfg, err := config.Load()
	if err != nil {
		log.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

	repository, err := metadata.NewSQLiteRepository(cfg.DatabaseDSN)
	if err != nil {
		log.Error("failed to initialize metadata repository", "error", err)
		os.Exit(1)
	}
	defer repository.Close()

	if err := repository.Migrate(ctx); err != nil {
		log.Error("failed to run metadata migrations", "error", err)
		os.Exit(1)
	}

	objectStorage, err := storage.NewR2Client(ctx, storage.R2ClientConfig{
		AccountID:       cfg.R2AccountID,
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
		cfg.ServerMaxUploadMB*1024*1024,
		cfg.AllowedMimeTypes,
		cfg.BlockedExtensions,
	)

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
		concurrencyLimiter,
		cfg.ServerMaxUploadMB*1024*1024,
		cfg.IPHashSalt,
		metrics.UploadsTotal,
		metrics.UploadBytesTotal,
		metrics.UploadErrorsTotal,
	)

	var cachePurger cloudflare.Purger
	if cfg.CloudflareCacheEnabled {
		cachePurger = cloudflare.NewClient(cfg.CloudflareZoneID, cfg.CloudflareAPIToken)
	}

	fileService := file.NewService(repository, objectStorage, cachePurger, cfg.CloudflareCacheEnabled, log)
	fileHandler := file.NewHandler(fileService)

	router := httpserver.NewRouter(httpserver.RouterDependencies{
		Logger:        log,
		UploadHandler: uploadHandler,
		FileHandler:   fileHandler,
		AllowedOrigin: cfg.AllowedOrigin,
	})

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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error("server_shutdown_error", "error", err)
	}
}

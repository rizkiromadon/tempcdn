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
	repository, err := metadata.NewRepository(rootCtx, cfg.DatabaseDSN)
	if err != nil {
		log.Error("failed to initialize metadata repository", "error", err)
		os.Exit(1)
	}
	defer repository.Close()

	if err := repository.Migrate(rootCtx); err != nil {
		log.Error("failed to run metadata migrations", "error", err)
		os.Exit(1)
	}

	objectStorage, err := storage.NewR2Client(rootCtx, storage.R2ClientConfig{
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
		log,
	)

	var cachePurger cloudflare.Purger
	if cfg.CloudflareCacheEnabled {
		cachePurger = cloudflare.NewClient(cfg.CloudflareZoneID, cfg.CloudflareAPIToken)
	}

	fileService := file.NewService(repository, objectStorage, cachePurger, cfg.CloudflareCacheEnabled, log)
	fileHandler := file.NewHandler(fileService)

	configHandler := upload.NewConfigHandler(
		cfg.ServerMaxUploadMB,
		cfg.AllowedMimeTypes,
		cfg.BlockedExtensions,
		cfg.FileTTLHours,
	)

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
		AllowedOrigin:     cfg.AllowedOrigin,
		MetricsToken:      cfg.MetricsToken,
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

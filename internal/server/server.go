// Package server provides a reusable server startup for the cache service.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"

	"github.com/falcosecurity/github-actions-cache-server/internal/config"
	"github.com/falcosecurity/github-actions-cache-server/internal/db"
	"github.com/falcosecurity/github-actions-cache-server/internal/handlers"
	"github.com/falcosecurity/github-actions-cache-server/internal/metrics"
	"github.com/falcosecurity/github-actions-cache-server/internal/storage"
	"github.com/falcosecurity/github-actions-cache-server/internal/tasks"
)

// App holds the cache server dependencies and HTTP server.
type App struct {
	cfg             *config.Config
	logger          *slog.Logger
	db              *db.DB
	storage         storage.Adapter
	scheduler       *tasks.Scheduler
	metricsShutdown func(context.Context) error
	httpServer      *http.Server
}

// New initializes all server dependencies and returns an App.
func New(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*App, error) {
	if cfg == nil {
		return nil, errors.New("config is nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	metricsShutdown, err := metrics.Init(ctx, metrics.Config{Enabled: cfg.MetricsEnabled})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize metrics: %w", err)
	}

	database, err := db.New(cfg)
	if err != nil {
		_ = metricsShutdown(ctx)
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	if err := database.Migrate(ctx); err != nil {
		database.Close()
		_ = metricsShutdown(ctx)
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	storageAdapter, err := initStorage(ctx, cfg)
	if err != nil {
		database.Close()
		_ = metricsShutdown(ctx)
		return nil, fmt.Errorf("failed to initialize storage: %w", err)
	}

	handler := handlers.New(cfg, database, storageAdapter, logger)

	scheduler := tasks.NewScheduler(cfg, database, storageAdapter, logger)
	if err := scheduler.Start(); err != nil {
		storageAdapter.Close()
		database.Close()
		_ = metricsShutdown(ctx)
		return nil, fmt.Errorf("failed to start scheduler: %w", err)
	}

	router := handler.Router()
	var httpHandler http.Handler = router
	if cfg.MetricsEnabled {
		httpHandler = metrics.HTTPMiddleware(router)
	}

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: httpHandler,
	}

	return &App{
		cfg:             cfg,
		logger:          logger,
		db:              database,
		storage:         storageAdapter,
		scheduler:       scheduler,
		metricsShutdown: metricsShutdown,
		httpServer:      server,
	}, nil
}

// Serve starts the HTTP server on the provided listener.
func (a *App) Serve(listener net.Listener) error {
	if a.httpServer == nil {
		return errors.New("server not initialized")
	}
	if listener == nil {
		return errors.New("listener is nil")
	}

	a.logger.Info("starting server", "addr", listener.Addr().String())
	if err := a.httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown stops the server and releases resources.
func (a *App) Shutdown(ctx context.Context) error {
	var errs []error

	if a.httpServer != nil {
		if err := a.httpServer.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs = append(errs, err)
		}
	}

	if a.scheduler != nil {
		a.scheduler.Stop()
	}

	if a.storage != nil {
		if err := a.storage.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if a.db != nil {
		if err := a.db.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if a.metricsShutdown != nil {
		if err := a.metricsShutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func initStorage(ctx context.Context, cfg *config.Config) (storage.Adapter, error) {
	switch cfg.StorageDriver {
	case "filesystem":
		return storage.NewFilesystemAdapter(cfg.StorageFilesystemPath, cfg.StorageHighWaterMark)
	case "s3":
		return storage.NewS3Adapter(ctx, storage.S3Config{
			Bucket:          cfg.StorageS3Bucket,
			Region:          cfg.AWSRegion,
			EndpointURL:     cfg.AWSEndpointURL,
			AccessKeyID:     cfg.AWSAccessKeyID,
			SecretAccessKey: cfg.AWSSecretAccessKey,
			HighWaterMark:   cfg.StorageHighWaterMark,
		})
	case "gcs":
		return storage.NewGCSAdapter(ctx, storage.GCSConfig{
			Bucket:            cfg.StorageGCSBucket,
			ServiceAccountKey: cfg.StorageGCSServiceAccountKey,
			Endpoint:          cfg.StorageGCSEndpoint,
			HighWaterMark:     cfg.StorageHighWaterMark,
		})
	default:
		return nil, fmt.Errorf("unsupported storage driver: %s", cfg.StorageDriver)
	}
}

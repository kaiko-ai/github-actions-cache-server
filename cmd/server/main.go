// Package main is the entry point for the GitHub Actions Cache Server.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/falcosecurity/github-actions-cache-server/internal/config"
	"github.com/falcosecurity/github-actions-cache-server/internal/server"
)

func main() {
	// Set up logging
	logLevel := slog.LevelInfo
	if os.Getenv("DEBUG") == "true" {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := run(ctx, logger); err != nil {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	logger.Info("configuration loaded",
		"storage_driver", cfg.StorageDriver,
		"db_driver", cfg.DBDriver,
		"port", cfg.Port,
	)

	app, err := server.New(ctx, cfg, logger)
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}
	defer listener.Close()

	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- app.Serve(listener)
	}()

	// Wait for shutdown signal
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErrors:
		if err != nil {
			return fmt.Errorf("server error: %w", err)
		}
	case sig := <-shutdown:
		logger.Info("shutdown signal received", "signal", sig)

		// Give outstanding requests 30 seconds to complete
		shutdownCtx, shutdownCancel := context.WithTimeout(ctx, 30*time.Second)
		defer shutdownCancel()

		if err := app.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown error: %w", err)
		}
	}

	return nil
}

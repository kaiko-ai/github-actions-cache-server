package tasks

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/falcosecurity/github-actions-cache-server/internal/config"
	"github.com/falcosecurity/github-actions-cache-server/internal/db"
	"github.com/falcosecurity/github-actions-cache-server/internal/storage"
)

func setupCleanupScheduler(t *testing.T) (*Scheduler, *db.DB, storage.Adapter) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "cleanup-task-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	storageAdapter, err := storage.NewFilesystemAdapter(tmpDir, 1024*1024)
	if err != nil {
		t.Fatalf("failed to create storage adapter: %v", err)
	}
	t.Cleanup(func() { storageAdapter.Close() })

	cfg := &config.Config{
		DBDriver:     "sqlite",
		DBSqlitePath: ":memory:",
	}

	database, err := db.New(cfg)
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewScheduler(cfg, database, storageAdapter, logger)
	return scheduler, database, storageAdapter
}

func createMergedLocation(t *testing.T, database *db.DB, folderName string, partCount int, mergedAt int64) string {
	t.Helper()

	locationID := uuid.New().String()
	loc := &db.StorageLocation{
		ID:         locationID,
		FolderName: folderName,
		PartCount:  partCount,
	}
	if err := database.CreateStorageLocation(context.Background(), loc); err != nil {
		t.Fatalf("failed to create storage location: %v", err)
	}
	if err := database.UpdateStorageLocationMerged(context.Background(), locationID, mergedAt); err != nil {
		t.Fatalf("failed to mark location merged: %v", err)
	}
	return locationID
}

func TestCleanupParts_RetentionWindow(t *testing.T) {
	scheduler, database, storageAdapter := setupCleanupScheduler(t)

	locationID := createMergedLocation(t, database, "folder-retention", 2, time.Now().UnixMilli())

	if err := storageAdapter.UploadStream(context.Background(), "folder-retention/merged", bytes.NewReader([]byte("merged"))); err != nil {
		t.Fatalf("failed to write merged object: %v", err)
	}
	if err := storageAdapter.UploadStream(context.Background(), "folder-retention/parts/0", bytes.NewReader([]byte("part-zero"))); err != nil {
		t.Fatalf("failed to write part 0: %v", err)
	}
	if err := storageAdapter.UploadStream(context.Background(), "folder-retention/parts/1", bytes.NewReader([]byte("part-one"))); err != nil {
		t.Fatalf("failed to write part 1: %v", err)
	}

	scheduler.cleanupParts()

	locAfterFirstRun, err := database.GetStorageLocation(context.Background(), locationID)
	if err != nil {
		t.Fatalf("failed to read location after first cleanup run: %v", err)
	}
	if locAfterFirstRun.PartsDeletedAt.Valid {
		t.Fatalf("expected parts to stay during retention window")
	}

	if _, err := storageAdapter.CreateDownloadStream(context.Background(), "folder-retention/parts/0"); err != nil {
		t.Fatalf("expected part to remain during retention window: %v", err)
	}

	oldMergedAt := time.Now().Add(-2 * time.Hour).UnixMilli()
	if err := database.UpdateStorageLocationMerged(context.Background(), locationID, oldMergedAt); err != nil {
		t.Fatalf("failed to backdate mergedAt timestamp: %v", err)
	}

	scheduler.cleanupParts()

	locAfterSecondRun, err := database.GetStorageLocation(context.Background(), locationID)
	if err != nil {
		t.Fatalf("failed to read location after second cleanup run: %v", err)
	}
	if !locAfterSecondRun.PartsDeletedAt.Valid {
		t.Fatalf("expected parts to be marked deleted after retention window")
	}

	_, err = storageAdapter.CreateDownloadStream(context.Background(), "folder-retention/parts/0")
	var notFoundErr *storage.ObjectNotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("expected parts to be deleted after retention, got err=%v", err)
	}
}

func TestCleanupParts_SkipsWhenMergedObjectMissing(t *testing.T) {
	scheduler, database, storageAdapter := setupCleanupScheduler(t)

	locationID := createMergedLocation(t, database, "folder-no-merged-object", 1, time.Now().Add(-time.Hour).UnixMilli())
	if err := storageAdapter.UploadStream(context.Background(), "folder-no-merged-object/parts/0", bytes.NewReader([]byte("part-zero"))); err != nil {
		t.Fatalf("failed to write part 0: %v", err)
	}

	scheduler.cleanupParts()

	locAfterRun, err := database.GetStorageLocation(context.Background(), locationID)
	if err != nil {
		t.Fatalf("failed to read location after cleanup run: %v", err)
	}
	if locAfterRun.PartsDeletedAt.Valid {
		t.Fatalf("expected partsDeletedAt to remain unset when merged object is missing")
	}

	partReader, err := storageAdapter.CreateDownloadStream(context.Background(), "folder-no-merged-object/parts/0")
	if err != nil {
		t.Fatalf("expected part to remain when merged object is missing: %v", err)
	}
	partReader.Close()
}

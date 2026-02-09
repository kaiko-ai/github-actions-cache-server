package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
)

func TestCompleteUploadOverwriteIntegrationPostgres(t *testing.T) {
	dsn := os.Getenv("DB_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set DB_TEST_POSTGRES_DSN to run postgres integration test")
	}
	runCompleteUploadOverwriteIntegration(t, "postgres", dsn)
}

func TestCompleteUploadOverwriteIntegrationMySQL(t *testing.T) {
	dsn := os.Getenv("DB_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set DB_TEST_MYSQL_DSN to run mysql integration test")
	}
	runCompleteUploadOverwriteIntegration(t, "mysql", dsn)
}

func runCompleteUploadOverwriteIntegration(t *testing.T, driver, dsn string) {
	t.Helper()

	sqlDB, err := sqlx.Connect(driver, dsn)
	if err != nil {
		t.Fatalf("failed to connect to %s: %v", driver, err)
	}

	database := &DB{DB: sqlDB, driver: driver}
	t.Cleanup(func() {
		dropAllTables(t, database)
		_ = database.Close()
	})

	dropAllTables(t, database)

	ctx := context.Background()
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("failed to migrate %s database: %v", driver, err)
	}
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("second migrate must be idempotent for %s: %v", driver, err)
	}

	loc1 := &StorageLocation{
		ID:         "loc-old",
		FolderName: "folder-old",
		PartCount:  1,
	}
	if err := database.CreateStorageLocation(ctx, loc1); err != nil {
		t.Fatalf("failed to create initial storage location: %v", err)
	}

	entry1 := &CacheEntry{
		ID:         "entry-existing",
		Key:        "test-key",
		Version:    "test-version",
		UpdatedAt:  time.Now().UnixMilli(),
		LocationID: loc1.ID,
	}
	if err := database.CreateCacheEntry(ctx, entry1); err != nil {
		t.Fatalf("failed to create initial cache entry: %v", err)
	}

	upload := &Upload{
		ID:         1234567890,
		Key:        "test-key",
		Version:    "test-version",
		FolderName: "folder-new",
		CreatedAt:  time.Now().UnixMilli(),
	}
	if err := database.CreateUpload(ctx, upload); err != nil {
		t.Fatalf("failed to create upload: %v", err)
	}

	loc2 := &StorageLocation{
		ID:         "loc-new",
		FolderName: "folder-new",
		PartCount:  3,
	}
	entry2 := &CacheEntry{
		ID:         "entry-new",
		Key:        upload.Key,
		Version:    upload.Version,
		UpdatedAt:  time.Now().UnixMilli(),
		LocationID: loc2.ID,
	}

	result, err := database.CompleteUpload(ctx, upload.ID, entry2, loc2)
	if err != nil {
		t.Fatalf("failed to complete upload overwrite for %s: %v", driver, err)
	}
	if result.OldFolderName != loc1.FolderName {
		t.Fatalf("expected old folder %q, got %q", loc1.FolderName, result.OldFolderName)
	}

	entryAfter, err := database.GetCacheEntryByKeyVersion(ctx, upload.Key, upload.Version)
	if err != nil {
		t.Fatalf("failed to read cache entry after overwrite: %v", err)
	}
	if entryAfter == nil {
		t.Fatalf("cache entry disappeared after overwrite")
	}
	if entryAfter.ID != entry1.ID {
		t.Fatalf("expected existing cache entry ID %q to be preserved, got %q", entry1.ID, entryAfter.ID)
	}
	if entryAfter.LocationID != loc2.ID {
		t.Fatalf("expected location %q, got %q", loc2.ID, entryAfter.LocationID)
	}

	oldLocation, err := database.GetStorageLocation(ctx, loc1.ID)
	if err != nil {
		t.Fatalf("failed to query old location: %v", err)
	}
	if oldLocation != nil {
		t.Fatalf("old storage location should be deleted")
	}

	newLocation, err := database.GetStorageLocation(ctx, loc2.ID)
	if err != nil {
		t.Fatalf("failed to query new location: %v", err)
	}
	if newLocation == nil {
		t.Fatalf("new storage location should exist")
	}
}

func dropAllTables(t *testing.T, database *DB) {
	t.Helper()

	ctx := context.Background()
	statements := []string{
		"DROP TABLE IF EXISTS cache_entries",
		"DROP TABLE IF EXISTS uploads",
		"DROP TABLE IF EXISTS storage_locations",
	}

	for _, stmt := range statements {
		if _, err := database.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("failed to execute cleanup statement %q: %v", stmt, err)
		}
	}
}

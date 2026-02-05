package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/falcosecurity/github-actions-cache-server/internal/config"
)

func setupTestDB(t *testing.T) (*DB, func()) {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()

	cfg := &config.Config{
		DBDriver:     "sqlite",
		DBSqlitePath: tmpFile.Name(),
		APIBaseURL:   "http://localhost:3000",
	}

	db, err := New(cfg)
	if err != nil {
		os.Remove(tmpFile.Name())
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		db.Close()
		os.Remove(tmpFile.Name())
		t.Fatal(err)
	}

	cleanup := func() {
		db.Close()
		os.Remove(tmpFile.Name())
	}

	return db, cleanup
}

func TestDB_UploadCRUD(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create upload
	upload := &Upload{
		ID:         1234567890,
		Key:        "test-key",
		Version:    "test-version",
		FolderName: "1234567890",
		CreatedAt:  time.Now().UnixMilli(),
	}

	err := db.CreateUpload(ctx, upload)
	if err != nil {
		t.Fatalf("failed to create upload: %v", err)
	}

	// Get upload by ID
	retrieved, err := db.GetUpload(ctx, upload.ID)
	if err != nil {
		t.Fatalf("failed to get upload: %v", err)
	}
	if retrieved == nil {
		t.Fatal("upload not found")
	}
	if retrieved.Key != upload.Key {
		t.Errorf("key mismatch: expected %s, got %s", upload.Key, retrieved.Key)
	}

	// Get upload by key/version
	retrieved, err = db.GetUploadByKeyVersion(ctx, upload.Key, upload.Version)
	if err != nil {
		t.Fatalf("failed to get upload by key/version: %v", err)
	}
	if retrieved == nil {
		t.Fatal("upload not found by key/version")
	}

	// Update last part uploaded
	now := time.Now().UnixMilli()
	err = db.UpdateUploadLastPart(ctx, upload.ID, now)
	if err != nil {
		t.Fatalf("failed to update last part: %v", err)
	}

	retrieved, _ = db.GetUpload(ctx, upload.ID)
	if !retrieved.LastPartUploadedAt.Valid || retrieved.LastPartUploadedAt.Int64 != now {
		t.Error("last part uploaded at not updated")
	}

	// Delete upload
	err = db.DeleteUpload(ctx, upload.ID)
	if err != nil {
		t.Fatalf("failed to delete upload: %v", err)
	}

	retrieved, _ = db.GetUpload(ctx, upload.ID)
	if retrieved != nil {
		t.Error("upload still exists after deletion")
	}
}

func TestDB_StorageLocationCRUD(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create storage location
	loc := &StorageLocation{
		ID:         "test-location-id",
		FolderName: "test-folder",
		PartCount:  5,
	}

	err := db.CreateStorageLocation(ctx, loc)
	if err != nil {
		t.Fatalf("failed to create storage location: %v", err)
	}

	// Get storage location
	retrieved, err := db.GetStorageLocation(ctx, loc.ID)
	if err != nil {
		t.Fatalf("failed to get storage location: %v", err)
	}
	if retrieved == nil {
		t.Fatal("storage location not found")
	}
	if retrieved.PartCount != loc.PartCount {
		t.Errorf("part count mismatch: expected %d, got %d", loc.PartCount, retrieved.PartCount)
	}

	// Update merge started
	now := time.Now().UnixMilli()
	err = db.UpdateStorageLocationMergeStarted(ctx, loc.ID, now)
	if err != nil {
		t.Fatalf("failed to update merge started: %v", err)
	}

	retrieved, _ = db.GetStorageLocation(ctx, loc.ID)
	if !retrieved.MergeStartedAt.Valid {
		t.Error("merge started at not set")
	}

	// Update merged
	err = db.UpdateStorageLocationMerged(ctx, loc.ID, now)
	if err != nil {
		t.Fatalf("failed to update merged: %v", err)
	}

	retrieved, _ = db.GetStorageLocation(ctx, loc.ID)
	if !retrieved.MergedAt.Valid {
		t.Error("merged at not set")
	}

	// Delete storage location
	err = db.DeleteStorageLocation(ctx, loc.ID)
	if err != nil {
		t.Fatalf("failed to delete storage location: %v", err)
	}

	retrieved, _ = db.GetStorageLocation(ctx, loc.ID)
	if retrieved != nil {
		t.Error("storage location still exists after deletion")
	}
}

func TestDB_CacheEntryCRUD(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// First create a storage location
	loc := &StorageLocation{
		ID:         "test-location-id",
		FolderName: "test-folder",
		PartCount:  1,
	}
	db.CreateStorageLocation(ctx, loc)

	// Create cache entry
	entry := &CacheEntry{
		ID:         "test-entry-id",
		Key:        "test-key",
		Version:    "test-version",
		UpdatedAt:  time.Now().UnixMilli(),
		LocationID: loc.ID,
	}

	err := db.CreateCacheEntry(ctx, entry)
	if err != nil {
		t.Fatalf("failed to create cache entry: %v", err)
	}

	// Get cache entry by ID
	retrieved, err := db.GetCacheEntry(ctx, entry.ID)
	if err != nil {
		t.Fatalf("failed to get cache entry: %v", err)
	}
	if retrieved == nil {
		t.Fatal("cache entry not found")
	}

	// Get cache entry by key/version
	retrieved, err = db.GetCacheEntryByKeyVersion(ctx, entry.Key, entry.Version)
	if err != nil {
		t.Fatalf("failed to get cache entry by key/version: %v", err)
	}
	if retrieved == nil {
		t.Fatal("cache entry not found by key/version")
	}

	// Delete cache entry
	err = db.DeleteCacheEntry(ctx, entry.ID)
	if err != nil {
		t.Fatalf("failed to delete cache entry: %v", err)
	}

	retrieved, _ = db.GetCacheEntry(ctx, entry.ID)
	if retrieved != nil {
		t.Error("cache entry still exists after deletion")
	}
}

func TestDB_GetCacheEntryByKeyVersionPrefix(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create storage location
	loc := &StorageLocation{
		ID:         "test-loc",
		FolderName: "test-folder",
		PartCount:  1,
	}
	db.CreateStorageLocation(ctx, loc)

	// Create multiple cache entries with similar keys
	now := time.Now().UnixMilli()
	entries := []*CacheEntry{
		{ID: "entry1", Key: "cache-key-v1", Version: "v1", UpdatedAt: now - 1000, LocationID: loc.ID},
		{ID: "entry2", Key: "cache-key-v2", Version: "v1", UpdatedAt: now, LocationID: loc.ID},
		{ID: "entry3", Key: "other-key", Version: "v1", UpdatedAt: now, LocationID: loc.ID},
	}

	for _, e := range entries {
		if err := db.CreateCacheEntry(ctx, e); err != nil {
			t.Fatalf("failed to create entry: %v", err)
		}
	}

	// Find by prefix - should return most recent
	found, err := db.GetCacheEntryByKeyVersionPrefix(ctx, "cache-key", "v1")
	if err != nil {
		t.Fatalf("failed to find by prefix: %v", err)
	}
	if found == nil {
		t.Fatal("expected to find entry by prefix")
	}
	if found.ID != "entry2" {
		t.Errorf("expected entry2 (most recent), got %s", found.ID)
	}

	// Non-matching prefix
	found, err = db.GetCacheEntryByKeyVersionPrefix(ctx, "nonexistent", "v1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found != nil {
		t.Error("expected nil for non-matching prefix")
	}
}

func TestDB_CompleteUpload(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create an upload
	upload := &Upload{
		ID:         1234567890,
		Key:        "test-key",
		Version:    "test-version",
		FolderName: "1234567890",
		CreatedAt:  time.Now().UnixMilli(),
	}
	db.CreateUpload(ctx, upload)

	// Complete the upload
	now := time.Now().UnixMilli()
	loc := &StorageLocation{
		ID:         "new-loc-id",
		FolderName: upload.FolderName,
		PartCount:  3,
	}
	entry := &CacheEntry{
		ID:         "new-entry-id",
		Key:        upload.Key,
		Version:    upload.Version,
		UpdatedAt:  now,
		LocationID: loc.ID,
	}

	result, err := db.CompleteUpload(ctx, upload.ID, entry, loc)
	if err != nil {
		t.Fatalf("failed to complete upload: %v", err)
	}
	if result.OldFolderName != "" {
		t.Error("expected no old folder name for new cache entry")
	}

	// Verify upload is deleted
	u, _ := db.GetUpload(ctx, upload.ID)
	if u != nil {
		t.Error("upload should be deleted after completion")
	}

	// Verify storage location exists
	l, _ := db.GetStorageLocation(ctx, loc.ID)
	if l == nil {
		t.Error("storage location should exist after completion")
	}

	// Verify cache entry exists
	e, _ := db.GetCacheEntry(ctx, entry.ID)
	if e == nil {
		t.Error("cache entry should exist after completion")
	}
}

func TestDB_CompleteUploadOverwrite(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create an initial cache entry
	loc1 := &StorageLocation{
		ID:         "loc1",
		FolderName: "folder1",
		PartCount:  1,
	}
	db.CreateStorageLocation(ctx, loc1)

	entry1 := &CacheEntry{
		ID:         "entry1",
		Key:        "test-key",
		Version:    "test-version",
		UpdatedAt:  time.Now().UnixMilli(),
		LocationID: loc1.ID,
	}
	db.CreateCacheEntry(ctx, entry1)

	// Create an upload for the same key/version
	upload := &Upload{
		ID:         1234567890,
		Key:        "test-key",
		Version:    "test-version",
		FolderName: "1234567890",
		CreatedAt:  time.Now().UnixMilli(),
	}
	db.CreateUpload(ctx, upload)

	// Complete the upload - should overwrite existing entry
	now := time.Now().UnixMilli()
	loc2 := &StorageLocation{
		ID:         "loc2",
		FolderName: upload.FolderName,
		PartCount:  3,
	}
	entry2 := &CacheEntry{
		ID:         "entry2",
		Key:        upload.Key,
		Version:    upload.Version,
		UpdatedAt:  now,
		LocationID: loc2.ID,
	}

	result, err := db.CompleteUpload(ctx, upload.ID, entry2, loc2)
	if err != nil {
		t.Fatalf("failed to complete upload: %v", err)
	}

	// Verify old folder name is returned for cleanup
	if result.OldFolderName != "folder1" {
		t.Errorf("expected old folder name 'folder1', got '%s'", result.OldFolderName)
	}

	// Verify old storage location is deleted
	l, _ := db.GetStorageLocation(ctx, loc1.ID)
	if l != nil {
		t.Error("old storage location should be deleted")
	}

	// Verify new storage location exists
	l, _ = db.GetStorageLocation(ctx, loc2.ID)
	if l == nil {
		t.Error("new storage location should exist")
	}

	// Verify cache entry was updated (existing entry keeps its ID)
	e, _ := db.GetCacheEntryByKeyVersion(ctx, "test-key", "test-version")
	if e == nil {
		t.Fatal("cache entry should exist")
	}
	if e.LocationID != loc2.ID {
		t.Errorf("expected locationId '%s', got '%s'", loc2.ID, e.LocationID)
	}
}

func TestDB_GetStaleUploads(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Use fixed timestamps for predictable behavior
	threshold := int64(1000000)
	oldTime := threshold - 1        // Just before threshold
	recentTime := threshold + 10000 // Well after threshold

	// Create uploads and set last_part_uploaded_at using UpdateUploadLastPart
	// since CreateUpload doesn't set that field

	// Upload 1: Stale - old created, no last part
	upload1 := &Upload{ID: 1, Key: "key1", Version: "v1", FolderName: "1", CreatedAt: oldTime}
	db.CreateUpload(ctx, upload1)

	// Upload 2: NOT stale - recent created
	upload2 := &Upload{ID: 2, Key: "key2", Version: "v1", FolderName: "2", CreatedAt: recentTime}
	db.CreateUpload(ctx, upload2)

	// Upload 3: NOT stale - old created but recent last part upload
	upload3 := &Upload{ID: 3, Key: "key3", Version: "v1", FolderName: "3", CreatedAt: oldTime}
	db.CreateUpload(ctx, upload3)
	db.UpdateUploadLastPart(ctx, 3, recentTime)

	// Upload 4: Stale - old created and old last part
	upload4 := &Upload{ID: 4, Key: "key4", Version: "v1", FolderName: "4", CreatedAt: oldTime}
	db.CreateUpload(ctx, upload4)
	db.UpdateUploadLastPart(ctx, 4, oldTime)

	stale, err := db.GetStaleUploads(ctx, threshold, 10)
	if err != nil {
		t.Fatalf("failed to get stale uploads: %v", err)
	}

	// Should get uploads ID 1 and 4 (both meet the stale criteria)
	if len(stale) != 2 {
		t.Errorf("expected 2 stale uploads, got %d", len(stale))
		for _, s := range stale {
			t.Logf("  stale upload ID=%d, created=%d, lastPart=%v", s.ID, s.CreatedAt, s.LastPartUploadedAt)
		}
	}

	// Verify we got the right ones
	staleIDs := make(map[int64]bool)
	for _, s := range stale {
		staleIDs[s.ID] = true
	}
	if !staleIDs[1] || !staleIDs[4] {
		t.Errorf("expected stale uploads 1 and 4, got %v", staleIDs)
	}
}

func TestDB_GetOrphanedStorageLocations(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create storage locations
	locs := []*StorageLocation{
		{ID: "loc1", FolderName: "folder1", PartCount: 1},
		{ID: "loc2", FolderName: "folder2", PartCount: 1},
	}
	for _, l := range locs {
		db.CreateStorageLocation(ctx, l)
	}

	// Create cache entry only for loc1
	entry := &CacheEntry{
		ID:         "entry1",
		Key:        "key1",
		Version:    "v1",
		UpdatedAt:  time.Now().UnixMilli(),
		LocationID: "loc1",
	}
	db.CreateCacheEntry(ctx, entry)

	// Get orphaned locations
	orphaned, err := db.GetOrphanedStorageLocations(ctx, 10)
	if err != nil {
		t.Fatalf("failed to get orphaned locations: %v", err)
	}

	if len(orphaned) != 1 {
		t.Errorf("expected 1 orphaned location, got %d", len(orphaned))
	}
	if len(orphaned) > 0 && orphaned[0].ID != "loc2" {
		t.Errorf("expected loc2 to be orphaned, got %s", orphaned[0].ID)
	}
}

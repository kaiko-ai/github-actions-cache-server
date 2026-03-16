package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/falcosecurity/github-actions-cache-server/internal/db"
)

func TestUI_DisabledByDefault(t *testing.T) {
	h := setupEndpointHandler(t)
	router := h.Router()

	// With UI disabled, root should return "OK" (not the HTML page)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if strings.TrimSpace(rr.Body.String()) != "OK" {
		t.Fatalf("expected root to return OK when UI disabled, got %q", rr.Body.String())
	}

	// API endpoints should fall through to catch-all when disabled
	rr2 := httptest.NewRecorder()
	router.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/api/cache-entries", nil))
	// Should NOT return JSON list (would be 200 with entries JSON if UI enabled)
	if rr2.Code == http.StatusOK {
		var resp listCacheEntriesResponse
		if err := json.Unmarshal(rr2.Body.Bytes(), &resp); err == nil {
			t.Fatal("expected API endpoint to be unavailable when UI disabled")
		}
	}
}

func TestUI_EnabledServesHTML(t *testing.T) {
	h := setupEndpointHandler(t)
	h.config.UIEnabled = true
	router := h.Router()

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for UI page, got %d", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if ct != "text/html; charset=utf-8" {
		t.Fatalf("expected text/html content type, got %q", ct)
	}
	if rr.Body.Len() == 0 {
		t.Fatal("expected non-empty body for UI page")
	}
}

func TestUI_ListEmpty(t *testing.T) {
	h := setupEndpointHandler(t)
	h.config.UIEnabled = true
	router := h.Router()

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/cache-entries", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}

	var resp listCacheEntriesResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Total != 0 {
		t.Fatalf("expected total=0, got %d", resp.Total)
	}
	if len(resp.Entries) != 0 {
		t.Fatalf("expected empty entries, got %d", len(resp.Entries))
	}
}

func TestUI_ListWithEntriesAndPagination(t *testing.T) {
	h := setupEndpointHandler(t)
	h.config.UIEnabled = true
	router := h.Router()

	// Create 3 entries
	createCacheEntryWithLocation(t, h, "key-a", "v1", "folder-a", 1)
	createCacheEntryWithLocation(t, h, "key-b", "v1", "folder-b", 2)
	createCacheEntryWithLocation(t, h, "key-c", "v1", "folder-c", 3)

	// List all
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/cache-entries?limit=50&offset=0", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp listCacheEntriesResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Total != 3 {
		t.Fatalf("expected total=3, got %d", resp.Total)
	}
	if len(resp.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(resp.Entries))
	}

	// Pagination: limit=2, offset=0
	rr2 := httptest.NewRecorder()
	router.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/api/cache-entries?limit=2&offset=0", nil))
	var resp2 listCacheEntriesResponse
	json.Unmarshal(rr2.Body.Bytes(), &resp2)
	if resp2.Total != 3 {
		t.Fatalf("expected total=3, got %d", resp2.Total)
	}
	if len(resp2.Entries) != 2 {
		t.Fatalf("expected 2 entries on page 1, got %d", len(resp2.Entries))
	}

	// Pagination: limit=2, offset=2
	rr3 := httptest.NewRecorder()
	router.ServeHTTP(rr3, httptest.NewRequest(http.MethodGet, "/api/cache-entries?limit=2&offset=2", nil))
	var resp3 listCacheEntriesResponse
	json.Unmarshal(rr3.Body.Bytes(), &resp3)
	if len(resp3.Entries) != 1 {
		t.Fatalf("expected 1 entry on page 2, got %d", len(resp3.Entries))
	}
}

func TestUI_ListWithKeyFilter(t *testing.T) {
	h := setupEndpointHandler(t)
	h.config.UIEnabled = true
	router := h.Router()

	createCacheEntryWithLocation(t, h, "linux-go-cache", "v1", "folder-lg", 1)
	createCacheEntryWithLocation(t, h, "linux-node-cache", "v1", "folder-ln", 1)
	createCacheEntryWithLocation(t, h, "windows-go-cache", "v1", "folder-wg", 1)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/cache-entries?key=linux-", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp listCacheEntriesResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Total != 2 {
		t.Fatalf("expected total=2 for linux- prefix, got %d", resp.Total)
	}
	for _, e := range resp.Entries {
		if e.Key != "linux-go-cache" && e.Key != "linux-node-cache" {
			t.Fatalf("unexpected key in filtered results: %q", e.Key)
		}
	}
}

func TestUI_ListShowsMergedStatus(t *testing.T) {
	h := setupEndpointHandler(t)
	h.config.UIEnabled = true
	router := h.Router()

	_, locID := createCacheEntryWithLocation(t, h, "key-merged", "v1", "folder-merged-status", 1)
	if err := h.db.UpdateStorageLocationMerged(context.Background(), locID, time.Now().UnixMilli()); err != nil {
		t.Fatalf("failed to mark merged: %v", err)
	}
	createCacheEntryWithLocation(t, h, "key-unmerged", "v1", "folder-unmerged-status", 1)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/cache-entries", nil))
	var resp listCacheEntriesResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)

	mergedFound := false
	unmergedFound := false
	for _, e := range resp.Entries {
		if e.Key == "key-merged" && e.Merged {
			mergedFound = true
		}
		if e.Key == "key-unmerged" && !e.Merged {
			unmergedFound = true
		}
	}
	if !mergedFound {
		t.Fatal("expected merged entry to have merged=true")
	}
	if !unmergedFound {
		t.Fatal("expected unmerged entry to have merged=false")
	}
}

func TestUI_DeleteSuccess(t *testing.T) {
	h := setupEndpointHandler(t)
	h.config.UIEnabled = true
	router := h.Router()

	entryID, locationID := createCacheEntryWithLocation(t, h, "key-to-delete", "v1", "folder-to-delete", 1)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/api/cache-entries/"+entryID, nil))

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d (%s)", rr.Code, rr.Body.String())
	}

	// Verify cache entry is gone
	entry, err := h.db.GetCacheEntry(context.Background(), entryID)
	if err != nil {
		t.Fatalf("failed to check cache entry: %v", err)
	}
	if entry != nil {
		t.Fatal("expected cache entry to be deleted")
	}

	// Verify storage location is gone
	loc, err := h.db.GetStorageLocation(context.Background(), locationID)
	if err != nil {
		t.Fatalf("failed to check storage location: %v", err)
	}
	if loc != nil {
		t.Fatal("expected storage location to be deleted")
	}
}

func TestUI_StatsEmpty(t *testing.T) {
	h := setupEndpointHandler(t)
	h.config.UIEnabled = true
	router := h.Router()

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/stats", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	var resp statsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.TotalEntries != 0 {
		t.Fatalf("expected 0 entries, got %d", resp.TotalEntries)
	}
	if resp.StorageDriver != "filesystem" {
		t.Fatalf("expected storage driver filesystem, got %q", resp.StorageDriver)
	}
	if resp.DBDriver != "sqlite" {
		t.Fatalf("expected db driver sqlite, got %q", resp.DBDriver)
	}
}

func TestUI_StatsWithEntries(t *testing.T) {
	h := setupEndpointHandler(t)
	h.config.UIEnabled = true
	h.config.CacheCleanupOlderThanDays = 30
	router := h.Router()

	// Create some entries — one merged, one unmerged
	_, locMerged := createCacheEntryWithLocation(t, h, "stats-merged", "v1", "folder-stats-m", 2)
	if err := h.db.UpdateStorageLocationMerged(context.Background(), locMerged, time.Now().UnixMilli()); err != nil {
		t.Fatalf("failed to mark merged: %v", err)
	}
	createCacheEntryWithLocation(t, h, "stats-unmerged", "v1", "folder-stats-u", 1)

	// Create an active upload
	createTestUpload(t, h, "stats-uploading", "v1", "folder-stats-up")

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/stats", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	var resp statsResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)

	if resp.TotalEntries != 2 {
		t.Fatalf("expected 2 total entries, got %d", resp.TotalEntries)
	}
	if resp.MergedEntries != 1 {
		t.Fatalf("expected 1 merged, got %d", resp.MergedEntries)
	}
	if resp.UnmergedEntries != 1 {
		t.Fatalf("expected 1 unmerged, got %d", resp.UnmergedEntries)
	}
	if resp.ActiveUploads != 1 {
		t.Fatalf("expected 1 active upload, got %d", resp.ActiveUploads)
	}
	if resp.OldestEntryAt == 0 {
		t.Fatal("expected oldest entry timestamp to be set")
	}
	if resp.NewestEntryAt == 0 {
		t.Fatal("expected newest entry timestamp to be set")
	}
	if resp.CleanupDays != 30 {
		t.Fatalf("expected cleanup days 30, got %d", resp.CleanupDays)
	}
}

func TestUI_StatsHealthIndicators(t *testing.T) {
	h := setupEndpointHandler(t)
	h.config.UIEnabled = true
	router := h.Router()

	// Create an entry with a stale merge (mergeStartedAt set long ago, mergedAt not set)
	_, locStale := createCacheEntryWithLocation(t, h, "stats-stale-merge", "v1", "folder-stale", 1)
	longAgo := time.Now().Add(-30 * time.Minute).UnixMilli()
	if err := h.db.UpdateStorageLocationMergeStarted(context.Background(), locStale, longAgo); err != nil {
		t.Fatalf("failed to set merge started: %v", err)
	}

	// Create a merged entry whose parts haven't been cleaned up yet
	_, locParts := createCacheEntryWithLocation(t, h, "stats-parts-pending", "v1", "folder-parts-pending", 3)
	if err := h.db.UpdateStorageLocationMerged(context.Background(), locParts, time.Now().UnixMilli()); err != nil {
		t.Fatalf("failed to mark merged: %v", err)
	}

	// Create an orphaned storage location (no cache entry pointing to it)
	orphanLoc := &db.StorageLocation{ID: "orphan-loc-1", FolderName: "folder-orphan", PartCount: 1}
	if err := h.db.CreateStorageLocation(context.Background(), orphanLoc); err != nil {
		t.Fatalf("failed to create orphan location: %v", err)
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/stats", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	var resp statsResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)

	if resp.CurrentlyMerging != 1 {
		t.Fatalf("expected 1 currently merging, got %d", resp.CurrentlyMerging)
	}
	if resp.StaleMerges != 1 {
		t.Fatalf("expected 1 stale merge, got %d", resp.StaleMerges)
	}
	if resp.PartsCleanupPending != 1 {
		t.Fatalf("expected 1 parts cleanup pending, got %d", resp.PartsCleanupPending)
	}
	if resp.OrphanedLocations != 1 {
		t.Fatalf("expected 1 orphaned location, got %d", resp.OrphanedLocations)
	}
}

func TestUI_DeleteNotFound(t *testing.T) {
	h := setupEndpointHandler(t)
	h.config.UIEnabled = true
	router := h.Router()

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/api/cache-entries/nonexistent-id", nil))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (%s)", rr.Code, rr.Body.String())
	}
}

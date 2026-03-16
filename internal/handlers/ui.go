package handlers

import (
	"embed"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

const staleMergeTimeout = 15 * time.Minute

//go:embed ui/index.html
var uiFS embed.FS

func (h *Handler) handleUIIndex(w http.ResponseWriter, r *http.Request) {
	data, err := uiFS.ReadFile("ui/index.html")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

type listCacheEntriesResponse struct {
	Entries []cacheEntryJSON `json:"entries"`
	Total   int              `json:"total"`
	Limit   int              `json:"limit"`
	Offset  int              `json:"offset"`
}

type cacheEntryJSON struct {
	ID               string `json:"id"`
	Key              string `json:"key"`
	Version          string `json:"version"`
	UpdatedAt        int64  `json:"updatedAt"`
	LocationID       string `json:"locationId"`
	FolderName       string `json:"folderName"`
	PartCount        int    `json:"partCount"`
	SizeBytes        int64  `json:"sizeBytes"`
	Merged           bool   `json:"merged"`
	LastDownloadedAt *int64 `json:"lastDownloadedAt"`
}

func (h *Handler) handleListCacheEntries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 500 {
		limit = 500
	}

	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	keyPrefix := r.URL.Query().Get("key")

	result, err := h.db.ListCacheEntries(ctx, keyPrefix, limit, offset)
	if err != nil {
		h.logger.Error("failed to list cache entries", "error", err)
		h.writeJSONError(w, http.StatusInternalServerError, "database error")
		return
	}

	entries := make([]cacheEntryJSON, 0, len(result.Entries))
	for _, e := range result.Entries {
		entry := cacheEntryJSON{
			ID:         e.ID,
			Key:        e.Key,
			Version:    e.Version,
			UpdatedAt:  e.UpdatedAt,
			LocationID: e.LocationID,
			FolderName: e.FolderName,
			PartCount:  e.PartCount,
			SizeBytes:  e.SizeBytes,
			Merged:     e.MergedAt.Valid,
		}
		if e.LastDownloadedAt.Valid {
			v := e.LastDownloadedAt.Int64
			entry.LastDownloadedAt = &v
		}
		entries = append(entries, entry)
	}

	h.writeJSON(w, http.StatusOK, listCacheEntriesResponse{
		Entries: entries,
		Total:   result.Total,
		Limit:   limit,
		Offset:  offset,
	})
}

type statsResponse struct {
	TotalEntries        int    `json:"totalEntries"`
	TotalSizeBytes      int64  `json:"totalSizeBytes"`
	MaxCacheSizeBytes   int64  `json:"maxCacheSizeBytes"`
	MergedEntries       int    `json:"mergedEntries"`
	UnmergedEntries     int    `json:"unmergedEntries"`
	ActiveUploads       int    `json:"activeUploads"`
	OldestEntryAt       int64  `json:"oldestEntryAt"`
	NewestEntryAt       int64  `json:"newestEntryAt"`
	CurrentlyMerging    int    `json:"currentlyMerging"`
	StaleMerges         int    `json:"staleMerges"`
	PartsCleanupPending int    `json:"partsCleanupPending"`
	OrphanedLocations   int    `json:"orphanedLocations"`
	StorageDriver       string `json:"storageDriver"`
	DBDriver            string `json:"dbDriver"`
	CleanupDays         int    `json:"cleanupDays"`
	CleanupDisabled     bool   `json:"cleanupDisabled"`
}

func (h *Handler) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	staleMergeThreshold := time.Now().Add(-staleMergeTimeout).UnixMilli()
	stats, err := h.db.GetCacheStats(ctx, staleMergeThreshold)
	if err != nil {
		h.logger.Error("failed to get cache stats", "error", err)
		h.writeJSONError(w, http.StatusInternalServerError, "database error")
		return
	}

	h.writeJSON(w, http.StatusOK, statsResponse{
		TotalEntries:        stats.TotalEntries,
		TotalSizeBytes:      stats.TotalSizeBytes,
		MaxCacheSizeBytes:   h.config.MaxCacheSizeBytes,
		MergedEntries:       stats.MergedEntries,
		UnmergedEntries:     stats.UnmergedEntries,
		ActiveUploads:       stats.ActiveUploads,
		OldestEntryAt:       stats.OldestEntryAt,
		NewestEntryAt:       stats.NewestEntryAt,
		CurrentlyMerging:    stats.CurrentlyMerging,
		StaleMerges:         stats.StaleMerges,
		PartsCleanupPending: stats.PartsCleanupPending,
		OrphanedLocations:   stats.OrphanedLocations,
		StorageDriver:       h.config.StorageDriver,
		DBDriver:            h.config.DBDriver,
		CleanupDays:         h.config.CacheCleanupOlderThanDays,
		CleanupDisabled:     h.config.DisableCleanupJobs,
	})
}

func (h *Handler) handleDeleteCacheEntry(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")

	entry, err := h.db.GetCacheEntry(ctx, id)
	if err != nil {
		h.logger.Error("failed to get cache entry", "error", err)
		h.writeJSONError(w, http.StatusInternalServerError, "database error")
		return
	}
	if entry == nil {
		h.writeJSONError(w, http.StatusNotFound, "cache entry not found")
		return
	}

	loc, err := h.db.GetStorageLocation(ctx, entry.LocationID)
	if err != nil {
		h.logger.Error("failed to get storage location", "error", err)
		h.writeJSONError(w, http.StatusInternalServerError, "database error")
		return
	}

	// Delete storage folder
	if loc != nil {
		if err := h.storage.DeleteFolder(ctx, loc.FolderName); err != nil {
			h.logger.Error("failed to delete storage folder", "error", err, "locationId", loc.ID)
		}
	}

	// Delete cache entries by location
	if err := h.db.DeleteCacheEntriesByLocationID(ctx, entry.LocationID); err != nil {
		h.logger.Error("failed to delete cache entries", "error", err)
		h.writeJSONError(w, http.StatusInternalServerError, "database error")
		return
	}

	// Delete storage location
	if err := h.db.DeleteStorageLocation(ctx, entry.LocationID); err != nil {
		h.logger.Error("failed to delete storage location", "error", err)
		h.writeJSONError(w, http.StatusInternalServerError, "database error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

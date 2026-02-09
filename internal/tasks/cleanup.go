// Package tasks provides scheduled cleanup tasks for the cache server.
package tasks

import (
	"context"
	"log/slog"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/falcosecurity/github-actions-cache-server/internal/config"
	"github.com/falcosecurity/github-actions-cache-server/internal/db"
	"github.com/falcosecurity/github-actions-cache-server/internal/metrics"
	"github.com/falcosecurity/github-actions-cache-server/internal/storage"
)

const (
	cleanupPageSize                 = 10
	uploadTimeout                   = 1 * time.Minute
	mergeTimeout                    = 15 * time.Minute
	defaultPartsRetentionAfterMerge = 1 * time.Hour
)

// Scheduler manages scheduled cleanup tasks.
type Scheduler struct {
	cron    *cron.Cron
	config  *config.Config
	db      *db.DB
	storage storage.Adapter
	logger  *slog.Logger
}

// NewScheduler creates a new task scheduler.
func NewScheduler(cfg *config.Config, database *db.DB, storageAdapter storage.Adapter, logger *slog.Logger) *Scheduler {
	return &Scheduler{
		cron:    cron.New(),
		config:  cfg,
		db:      database,
		storage: storageAdapter,
		logger:  logger,
	}
}

// Start starts all scheduled tasks.
func (s *Scheduler) Start() error {
	if s.config.DisableCleanupJobs {
		s.logger.Info("cleanup jobs disabled")
		return nil
	}

	// Uploads cleanup - every 5 minutes
	if _, err := s.cron.AddFunc("*/5 * * * *", s.cleanupUploads); err != nil {
		return err
	}

	// Cache entries cleanup - daily at 00:00
	if _, err := s.cron.AddFunc("0 0 * * *", s.cleanupCacheEntries); err != nil {
		return err
	}

	// Storage locations cleanup - daily at 00:00
	if _, err := s.cron.AddFunc("0 0 * * *", s.cleanupStorageLocations); err != nil {
		return err
	}

	// Parts cleanup - hourly at 00 min
	if _, err := s.cron.AddFunc("0 * * * *", s.cleanupParts); err != nil {
		return err
	}

	// Merges cleanup - hourly at 00 min
	if _, err := s.cron.AddFunc("0 * * * *", s.cleanupMerges); err != nil {
		return err
	}

	s.cron.Start()
	s.logger.Info("cleanup scheduler started")
	return nil
}

// Stop stops the scheduler.
func (s *Scheduler) Stop() {
	s.cron.Stop()
}

// recordCleanup records cleanup operation metrics.
func (s *Scheduler) recordCleanup(ctx context.Context, operation, status string, start time.Time, processed, deleted int64) {
	if m := metrics.Get(); m != nil {
		m.RecordCleanupOperation(ctx, operation, status, time.Since(start), processed, deleted)
	}
}

func (s *Scheduler) recordCleanupSkip(ctx context.Context, operation, reason string) {
	if m := metrics.Get(); m != nil {
		m.RecordCleanupItem(ctx, operation, reason, 1)
	}
}

// cleanupUploads deletes stale uploads.
func (s *Scheduler) cleanupUploads() {
	ctx := context.Background()
	start := time.Now()
	s.logger.Debug("running uploads cleanup")

	totalProcessed := int64(0)
	totalDeleted := int64(0)
	threshold := time.Now().Add(-uploadTimeout).UnixMilli()
	status := "success"

	for {
		uploads, err := s.db.GetStaleUploads(ctx, threshold, cleanupPageSize)
		if err != nil {
			s.logger.Error("failed to get stale uploads", "error", err)
			status = "failure"
			break
		}

		if len(uploads) == 0 {
			break
		}

		totalProcessed += int64(len(uploads))

		for _, upload := range uploads {
			// Delete storage folder
			if err := s.storage.DeleteFolder(ctx, upload.FolderName); err != nil {
				s.logger.Error("failed to delete upload folder", "error", err, "uploadId", upload.ID)
				continue
			}

			// Delete database record
			if err := s.db.DeleteUpload(ctx, upload.ID); err != nil {
				s.logger.Error("failed to delete upload record", "error", err, "uploadId", upload.ID)
				continue
			}

			totalDeleted++
		}
	}

	s.recordCleanup(ctx, "uploads", status, start, totalProcessed, totalDeleted)

	if totalDeleted > 0 {
		s.logger.Info("uploads cleanup completed", "deleted", totalDeleted)
	}
}

// cleanupCacheEntries deletes old cache entries.
func (s *Scheduler) cleanupCacheEntries() {
	ctx := context.Background()
	start := time.Now()
	s.logger.Debug("running cache entries cleanup")

	totalProcessed := int64(0)
	totalDeleted := int64(0)
	threshold := time.Now().AddDate(0, 0, -s.config.CacheCleanupOlderThanDays).UnixMilli()
	status := "success"

	for {
		locations, err := s.db.GetOldCacheEntries(ctx, threshold, cleanupPageSize)
		if err != nil {
			s.logger.Error("failed to get old cache entries", "error", err)
			status = "failure"
			break
		}

		if len(locations) == 0 {
			break
		}

		totalProcessed += int64(len(locations))

		for _, loc := range locations {
			// Delete storage folder
			if err := s.storage.DeleteFolder(ctx, loc.FolderName); err != nil {
				s.logger.Error("failed to delete storage folder", "error", err, "locationId", loc.ID)
				continue
			}

			// Delete cache entries for this location
			if err := s.db.DeleteCacheEntriesByLocationID(ctx, loc.ID); err != nil {
				s.logger.Error("failed to delete cache entries", "error", err, "locationId", loc.ID)
				continue
			}

			// Delete storage location
			if err := s.db.DeleteStorageLocation(ctx, loc.ID); err != nil {
				s.logger.Error("failed to delete storage location", "error", err, "locationId", loc.ID)
				continue
			}

			totalDeleted++
		}
	}

	s.recordCleanup(ctx, "cache_entries", status, start, totalProcessed, totalDeleted)

	if totalDeleted > 0 {
		s.logger.Info("cache entries cleanup completed", "deleted", totalDeleted)
	}
}

// cleanupStorageLocations deletes orphaned storage locations.
func (s *Scheduler) cleanupStorageLocations() {
	ctx := context.Background()
	start := time.Now()
	s.logger.Debug("running storage locations cleanup")

	totalProcessed := int64(0)
	totalDeleted := int64(0)
	status := "success"

	for {
		locations, err := s.db.GetOrphanedStorageLocations(ctx, cleanupPageSize)
		if err != nil {
			s.logger.Error("failed to get orphaned storage locations", "error", err)
			status = "failure"
			break
		}

		if len(locations) == 0 {
			break
		}

		totalProcessed += int64(len(locations))

		for _, loc := range locations {
			// Delete storage folder
			if err := s.storage.DeleteFolder(ctx, loc.FolderName); err != nil {
				s.logger.Error("failed to delete storage folder", "error", err, "locationId", loc.ID)
				continue
			}

			// Delete storage location
			if err := s.db.DeleteStorageLocation(ctx, loc.ID); err != nil {
				s.logger.Error("failed to delete storage location", "error", err, "locationId", loc.ID)
				continue
			}

			totalDeleted++
		}
	}

	s.recordCleanup(ctx, "storage_locations", status, start, totalProcessed, totalDeleted)

	if totalDeleted > 0 {
		s.logger.Info("storage locations cleanup completed", "deleted", totalDeleted)
	}
}

// cleanupParts deletes parts for merged cache entries.
func (s *Scheduler) cleanupParts() {
	ctx := context.Background()
	start := time.Now()
	s.logger.Debug("running parts cleanup")

	totalProcessed := int64(0)
	totalDeleted := int64(0)
	now := time.Now()
	nowUnixMilli := now.UnixMilli()
	status := "success"

	for {
		locations, err := s.db.GetMergedStorageLocationsForPartsCleanup(ctx, cleanupPageSize)
		if err != nil {
			s.logger.Error("failed to get merged storage locations", "error", err)
			status = "failure"
			break
		}

		if len(locations) == 0 {
			break
		}

		totalProcessed += int64(len(locations))
		deletedThisPage := int64(0)

		for _, loc := range locations {
			mergedObjectName := loc.FolderName + "/merged"
			mergedReader, err := s.storage.CreateDownloadStream(ctx, mergedObjectName)
			if err != nil {
				s.recordCleanupSkip(ctx, "parts", "skip_no_merged")
				s.logger.Warn("skipping parts cleanup because merged object is unavailable", "locationId", loc.ID, "folder", loc.FolderName, "error", err)
				continue
			}
			mergedReader.Close()

			if !loc.MergedAt.Valid {
				s.recordCleanupSkip(ctx, "parts", "skip_not_merged")
				continue
			}

			mergedAt := time.UnixMilli(loc.MergedAt.Int64)
			if now.Sub(mergedAt) < defaultPartsRetentionAfterMerge {
				s.recordCleanupSkip(ctx, "parts", "skip_retention")
				s.logger.Debug("skipping parts cleanup because retention window is active", "locationId", loc.ID, "mergedAt", loc.MergedAt.Int64, "retention", defaultPartsRetentionAfterMerge.String())
				continue
			}

			// Delete parts folder
			partsFolder := loc.FolderName + "/parts"
			if err := s.storage.DeleteFolder(ctx, partsFolder); err != nil {
				s.logger.Error("failed to delete parts folder", "error", err, "locationId", loc.ID)
				continue
			}

			// Mark parts as deleted
			if err := s.db.UpdateStorageLocationPartsDeleted(ctx, loc.ID, nowUnixMilli); err != nil {
				s.logger.Error("failed to mark parts deleted", "error", err, "locationId", loc.ID)
				continue
			}

			totalDeleted += int64(loc.PartCount)
			deletedThisPage++
			if m := metrics.Get(); m != nil {
				m.RecordCleanupItem(ctx, "parts", "deleted", int64(loc.PartCount))
			}
		}

		// If nothing was deleted, stop to avoid looping forever on the same skipped rows.
		if deletedThisPage == 0 {
			break
		}
	}

	s.recordCleanup(ctx, "parts", status, start, totalProcessed, totalDeleted)

	if totalDeleted > 0 {
		s.logger.Info("parts cleanup completed", "deleted", totalDeleted)
	}
}

// cleanupMerges resets stalled merges.
func (s *Scheduler) cleanupMerges() {
	ctx := context.Background()
	start := time.Now()
	s.logger.Debug("running merges cleanup")

	threshold := time.Now().Add(-mergeTimeout).UnixMilli()
	status := "success"

	updated, err := s.db.GetStaleMerges(ctx, threshold)
	if err != nil {
		s.logger.Error("failed to reset stale merges", "error", err)
		status = "failure"
		s.recordCleanup(ctx, "merges", status, start, 0, 0)
		return
	}

	s.recordCleanup(ctx, "merges", status, start, int64(updated), int64(updated))

	if updated > 0 {
		s.logger.Info("merges cleanup completed", "updated", updated)
	}
}

// Package tasks provides scheduled cleanup tasks for the cache server.
package tasks

import (
	"context"
	"log/slog"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/falcosecurity/github-actions-cache-server/internal/config"
	"github.com/falcosecurity/github-actions-cache-server/internal/db"
	"github.com/falcosecurity/github-actions-cache-server/internal/storage"
)

const (
	cleanupPageSize = 10
	uploadTimeout   = 1 * time.Minute
	mergeTimeout    = 15 * time.Minute
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

// cleanupUploads deletes stale uploads.
func (s *Scheduler) cleanupUploads() {
	ctx := context.Background()
	s.logger.Debug("running uploads cleanup")

	totalDeleted := 0
	threshold := time.Now().Add(-uploadTimeout).UnixMilli()

	for {
		uploads, err := s.db.GetStaleUploads(ctx, threshold, cleanupPageSize)
		if err != nil {
			s.logger.Error("failed to get stale uploads", "error", err)
			return
		}

		if len(uploads) == 0 {
			break
		}

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

	if totalDeleted > 0 {
		s.logger.Info("uploads cleanup completed", "deleted", totalDeleted)
	}
}

// cleanupCacheEntries deletes old cache entries.
func (s *Scheduler) cleanupCacheEntries() {
	ctx := context.Background()
	s.logger.Debug("running cache entries cleanup")

	totalDeleted := 0
	threshold := time.Now().AddDate(0, 0, -s.config.CacheCleanupOlderThanDays).UnixMilli()

	for {
		locations, err := s.db.GetOldCacheEntries(ctx, threshold, cleanupPageSize)
		if err != nil {
			s.logger.Error("failed to get old cache entries", "error", err)
			return
		}

		if len(locations) == 0 {
			break
		}

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

	if totalDeleted > 0 {
		s.logger.Info("cache entries cleanup completed", "deleted", totalDeleted)
	}
}

// cleanupStorageLocations deletes orphaned storage locations.
func (s *Scheduler) cleanupStorageLocations() {
	ctx := context.Background()
	s.logger.Debug("running storage locations cleanup")

	totalDeleted := 0

	for {
		locations, err := s.db.GetOrphanedStorageLocations(ctx, cleanupPageSize)
		if err != nil {
			s.logger.Error("failed to get orphaned storage locations", "error", err)
			return
		}

		if len(locations) == 0 {
			break
		}

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

	if totalDeleted > 0 {
		s.logger.Info("storage locations cleanup completed", "deleted", totalDeleted)
	}
}

// cleanupParts deletes parts for merged cache entries.
func (s *Scheduler) cleanupParts() {
	ctx := context.Background()
	s.logger.Debug("running parts cleanup")

	totalDeleted := 0
	now := time.Now().UnixMilli()

	for {
		locations, err := s.db.GetMergedStorageLocationsForPartsCleanup(ctx, cleanupPageSize)
		if err != nil {
			s.logger.Error("failed to get merged storage locations", "error", err)
			return
		}

		if len(locations) == 0 {
			break
		}

		for _, loc := range locations {
			// Delete parts folder
			partsFolder := loc.FolderName + "/parts"
			if err := s.storage.DeleteFolder(ctx, partsFolder); err != nil {
				s.logger.Error("failed to delete parts folder", "error", err, "locationId", loc.ID)
				continue
			}

			// Mark parts as deleted
			if err := s.db.UpdateStorageLocationPartsDeleted(ctx, loc.ID, now); err != nil {
				s.logger.Error("failed to mark parts deleted", "error", err, "locationId", loc.ID)
				continue
			}

			totalDeleted += loc.PartCount
		}
	}

	if totalDeleted > 0 {
		s.logger.Info("parts cleanup completed", "deleted", totalDeleted)
	}
}

// cleanupMerges resets stalled merges.
func (s *Scheduler) cleanupMerges() {
	ctx := context.Background()
	s.logger.Debug("running merges cleanup")

	threshold := time.Now().Add(-mergeTimeout).UnixMilli()

	updated, err := s.db.GetStaleMerges(ctx, threshold)
	if err != nil {
		s.logger.Error("failed to reset stale merges", "error", err)
		return
	}

	if updated > 0 {
		s.logger.Info("merges cleanup completed", "updated", updated)
	}
}

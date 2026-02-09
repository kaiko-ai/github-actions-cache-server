// Package db provides database abstraction for the cache server.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql" // MySQL driver
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"           // PostgreSQL driver
	_ "github.com/mattn/go-sqlite3" // SQLite driver

	"github.com/falcosecurity/github-actions-cache-server/internal/config"
	"github.com/falcosecurity/github-actions-cache-server/internal/metrics"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB wraps sqlx.DB with additional functionality.
type DB struct {
	*sqlx.DB
	driver string
}

func (d *DB) recordDBQuery(ctx context.Context, table string, start time.Time) {
	if m := metrics.Get(); m != nil {
		m.RecordDBQuery(ctx, table, time.Since(start))
	}
}

// CacheEntry represents a cache entry in the database.
type CacheEntry struct {
	ID         string `db:"id"`
	Key        string `db:"key"`
	Version    string `db:"version"`
	UpdatedAt  int64  `db:"updatedAt"`
	LocationID string `db:"locationId"`
}

// StorageLocation represents a storage location in the database.
type StorageLocation struct {
	ID               string        `db:"id"`
	FolderName       string        `db:"folderName"`
	PartCount        int           `db:"partCount"`
	MergeStartedAt   sql.NullInt64 `db:"mergeStartedAt"`
	MergedAt         sql.NullInt64 `db:"mergedAt"`
	PartsDeletedAt   sql.NullInt64 `db:"partsDeletedAt"`
	LastDownloadedAt sql.NullInt64 `db:"lastDownloadedAt"`
}

// Upload represents an upload in progress.
type Upload struct {
	ID                 int64         `db:"id"`
	Key                string        `db:"key"`
	Version            string        `db:"version"`
	FolderName         string        `db:"folderName"`
	CreatedAt          int64         `db:"createdAt"`
	LastPartUploadedAt sql.NullInt64 `db:"lastPartUploadedAt"`
	UploadedBytes      int64         `db:"uploadedBytes"`
	UploadedParts      int           `db:"uploadedParts"`
}

// New creates a new database connection based on configuration.
func New(cfg *config.Config) (*DB, error) {
	var db *sqlx.DB
	var err error
	var driver string

	switch cfg.DBDriver {
	case "sqlite":
		driver = "sqlite3"
		db, err = sqlx.Connect(driver, cfg.DBSqlitePath)
	case "postgres":
		driver = "postgres"
		db, err = sqlx.Connect(driver, cfg.GetPostgresConnectionString())
	case "mysql":
		driver = "mysql"
		db, err = sqlx.Connect(driver, cfg.GetMySQLConnectionString())
	default:
		return nil, fmt.Errorf("unsupported database driver: %s", cfg.DBDriver)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Set connection pool settings.
	maxOpenConns := cfg.DBMaxOpenConns
	if maxOpenConns <= 0 {
		maxOpenConns = 10
	}

	maxIdleConns := cfg.DBMaxIdleConns
	if maxIdleConns < 0 {
		maxIdleConns = 0
	}
	if maxIdleConns > maxOpenConns {
		maxIdleConns = maxOpenConns
	}

	// SQLite in-memory databases are per-connection; use a single connection to keep
	// schema and data visible across queries.
	if driver == "sqlite3" && strings.Contains(cfg.DBSqlitePath, ":memory:") {
		maxOpenConns = 1
		maxIdleConns = 1
	}

	connMaxLifetime := time.Duration(cfg.DBConnMaxLifetimeSeconds) * time.Second
	if connMaxLifetime <= 0 {
		connMaxLifetime = time.Hour
	}

	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)
	db.SetConnMaxLifetime(connMaxLifetime)

	return &DB{DB: db, driver: driver}, nil
}

// Driver returns the database driver name.
func (d *DB) Driver() string {
	return d.driver
}

// Migrate runs database migrations.
func (d *DB) Migrate(ctx context.Context) error {
	var migrationFile string
	switch d.driver {
	case "sqlite3":
		migrationFile = "migrations/sqlite.sql"
	case "postgres":
		migrationFile = "migrations/postgres.sql"
	case "mysql":
		migrationFile = "migrations/mysql.sql"
	default:
		return fmt.Errorf("unsupported database driver for migrations: %s", d.driver)
	}

	content, err := migrationsFS.ReadFile(migrationFile)
	if err != nil {
		return fmt.Errorf("failed to read migration file: %w", err)
	}

	statements := strings.Split(string(content), ";")
	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := d.ExecContext(ctx, stmt); err != nil {
			if d.isIgnorableMigrationError(stmt, err) {
				continue
			}
			return fmt.Errorf("failed to execute migration statement: %w\nStatement: %s", err, stmt)
		}
	}

	return nil
}

func (d *DB) isIgnorableMigrationError(stmt string, err error) bool {
	stmtLower := strings.ToLower(strings.TrimSpace(stmt))
	errLower := strings.ToLower(err.Error())

	if strings.HasPrefix(stmtLower, "create index") {
		return strings.Contains(errLower, "already exists") || strings.Contains(errLower, "duplicate key name")
	}

	if strings.HasPrefix(stmtLower, "alter table") && strings.Contains(stmtLower, "add column") {
		return strings.Contains(errLower, "duplicate column") || strings.Contains(errLower, "already exists")
	}

	return false
}

// Placeholder returns the appropriate placeholder for the database driver.
func (d *DB) Placeholder(index int) string {
	switch d.driver {
	case "postgres":
		return fmt.Sprintf("$%d", index)
	default:
		return "?"
	}
}

// Rebind transforms a query from ? placeholders to the appropriate driver format.
func (d *DB) Rebind(query string) string {
	return d.DB.Rebind(query)
}

// QuoteIdent returns the quoted identifier for the database driver.
// This is needed for reserved words like "key".
func (d *DB) QuoteIdent(ident string) string {
	switch d.driver {
	case "mysql":
		return "`" + ident + "`"
	case "postgres":
		return `"` + ident + `"`
	default:
		return ident
	}
}

// keyCol returns the properly quoted "key" column name for the driver.
func (d *DB) keyCol() string {
	if d.driver == "mysql" {
		return "`key`"
	}
	return "key"
}

// col returns the properly quoted column name for the driver.
// PostgreSQL requires quoting camelCase column names.
func (d *DB) col(name string) string {
	if d.driver == "postgres" {
		return `"` + name + `"`
	}
	return name
}

// CreateUpload creates a new upload record.
func (d *DB) CreateUpload(ctx context.Context, upload *Upload) error {
	query := fmt.Sprintf(
		`INSERT INTO uploads (id, %s, version, %s, %s, %s, %s) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		d.keyCol(),
		d.col("folderName"),
		d.col("createdAt"),
		d.col("uploadedBytes"),
		d.col("uploadedParts"),
	)
	start := time.Now()
	_, err := d.ExecContext(
		ctx,
		d.Rebind(query),
		upload.ID,
		upload.Key,
		upload.Version,
		upload.FolderName,
		upload.CreatedAt,
		upload.UploadedBytes,
		upload.UploadedParts,
	)
	d.recordDBQuery(ctx, "uploads", start)
	return err
}

// GetUpload retrieves an upload by ID.
func (d *DB) GetUpload(ctx context.Context, id int64) (*Upload, error) {
	var upload Upload
	query := fmt.Sprintf(
		`SELECT id, %s, version, %s, %s, %s, %s, %s FROM uploads WHERE id = ?`,
		d.keyCol(),
		d.col("folderName"),
		d.col("createdAt"),
		d.col("lastPartUploadedAt"),
		d.col("uploadedBytes"),
		d.col("uploadedParts"),
	)
	start := time.Now()
	err := d.GetContext(ctx, &upload, d.Rebind(query), id)
	d.recordDBQuery(ctx, "uploads", start)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &upload, err
}

// UpdateUploadLastPart updates the lastPartUploadedAt timestamp.
func (d *DB) UpdateUploadLastPart(ctx context.Context, id int64, timestamp int64) error {
	query := fmt.Sprintf(`UPDATE uploads SET %s = ? WHERE id = ?`, d.col("lastPartUploadedAt"))
	start := time.Now()
	_, err := d.ExecContext(ctx, d.Rebind(query), timestamp, id)
	d.recordDBQuery(ctx, "uploads", start)
	return err
}

// UpdateUploadProgress updates upload progress after a successfully persisted chunk.
func (d *DB) UpdateUploadProgress(ctx context.Context, id int64, timestamp int64, bytesDelta int64) error {
	if bytesDelta < 0 {
		bytesDelta = 0
	}

	query := fmt.Sprintf(
		`UPDATE uploads SET %s = ?, %s = %s + ?, %s = %s + 1 WHERE id = ?`,
		d.col("lastPartUploadedAt"),
		d.col("uploadedBytes"),
		d.col("uploadedBytes"),
		d.col("uploadedParts"),
		d.col("uploadedParts"),
	)
	start := time.Now()
	_, err := d.ExecContext(ctx, d.Rebind(query), timestamp, bytesDelta, id)
	d.recordDBQuery(ctx, "uploads", start)
	return err
}

// DeleteUpload deletes an upload by ID.
func (d *DB) DeleteUpload(ctx context.Context, id int64) error {
	query := `DELETE FROM uploads WHERE id = ?`
	start := time.Now()
	_, err := d.ExecContext(ctx, d.Rebind(query), id)
	d.recordDBQuery(ctx, "uploads", start)
	return err
}

// GetUploadByKeyVersion retrieves an upload by key and version.
func (d *DB) GetUploadByKeyVersion(ctx context.Context, key, version string) (*Upload, error) {
	var upload Upload
	query := fmt.Sprintf(
		`SELECT id, %[1]s, version, %[2]s, %[3]s, %[4]s, %[5]s, %[6]s FROM uploads WHERE %[1]s = ? AND version = ?`,
		d.keyCol(),
		d.col("folderName"),
		d.col("createdAt"),
		d.col("lastPartUploadedAt"),
		d.col("uploadedBytes"),
		d.col("uploadedParts"),
	)
	start := time.Now()
	err := d.GetContext(ctx, &upload, d.Rebind(query), key, version)
	d.recordDBQuery(ctx, "uploads", start)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &upload, err
}

// CreateStorageLocation creates a new storage location.
func (d *DB) CreateStorageLocation(ctx context.Context, loc *StorageLocation) error {
	query := fmt.Sprintf(`INSERT INTO storage_locations (id, %s, %s) VALUES (?, ?, ?)`,
		d.col("folderName"), d.col("partCount"))
	start := time.Now()
	_, err := d.ExecContext(ctx, d.Rebind(query), loc.ID, loc.FolderName, loc.PartCount)
	d.recordDBQuery(ctx, "storage_locations", start)
	return err
}

// GetStorageLocation retrieves a storage location by ID.
func (d *DB) GetStorageLocation(ctx context.Context, id string) (*StorageLocation, error) {
	var loc StorageLocation
	query := fmt.Sprintf(`SELECT id, %s, %s, %s, %s, %s, %s FROM storage_locations WHERE id = ?`,
		d.col("folderName"), d.col("partCount"), d.col("mergeStartedAt"),
		d.col("mergedAt"), d.col("partsDeletedAt"), d.col("lastDownloadedAt"))
	start := time.Now()
	err := d.GetContext(ctx, &loc, d.Rebind(query), id)
	d.recordDBQuery(ctx, "storage_locations", start)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &loc, err
}

// UpdateStorageLocationPartCount updates the part count for a storage location.
func (d *DB) UpdateStorageLocationPartCount(ctx context.Context, id string, partCount int) error {
	query := fmt.Sprintf(`UPDATE storage_locations SET %s = ? WHERE id = ?`, d.col("partCount"))
	start := time.Now()
	_, err := d.ExecContext(ctx, d.Rebind(query), partCount, id)
	d.recordDBQuery(ctx, "storage_locations", start)
	return err
}

// UpdateStorageLocationMergeStarted marks a merge as started.
func (d *DB) UpdateStorageLocationMergeStarted(ctx context.Context, id string, timestamp int64) error {
	query := fmt.Sprintf(`UPDATE storage_locations SET %s = ? WHERE id = ?`, d.col("mergeStartedAt"))
	start := time.Now()
	_, err := d.ExecContext(ctx, d.Rebind(query), timestamp, id)
	d.recordDBQuery(ctx, "storage_locations", start)
	return err
}

// UpdateStorageLocationMerged marks a storage location as merged.
func (d *DB) UpdateStorageLocationMerged(ctx context.Context, id string, timestamp int64) error {
	query := fmt.Sprintf(`UPDATE storage_locations SET %s = ? WHERE id = ?`, d.col("mergedAt"))
	start := time.Now()
	_, err := d.ExecContext(ctx, d.Rebind(query), timestamp, id)
	d.recordDBQuery(ctx, "storage_locations", start)
	return err
}

// ResetStorageLocationMergeState clears mergeStartedAt and mergedAt.
func (d *DB) ResetStorageLocationMergeState(ctx context.Context, id string) error {
	query := fmt.Sprintf(`UPDATE storage_locations SET %s = NULL, %s = NULL WHERE id = ?`,
		d.col("mergeStartedAt"), d.col("mergedAt"))
	start := time.Now()
	_, err := d.ExecContext(ctx, d.Rebind(query), id)
	d.recordDBQuery(ctx, "storage_locations", start)
	return err
}

// UpdateStorageLocationPartsDeleted marks parts as deleted.
func (d *DB) UpdateStorageLocationPartsDeleted(ctx context.Context, id string, timestamp int64) error {
	query := fmt.Sprintf(`UPDATE storage_locations SET %s = ? WHERE id = ?`, d.col("partsDeletedAt"))
	start := time.Now()
	_, err := d.ExecContext(ctx, d.Rebind(query), timestamp, id)
	d.recordDBQuery(ctx, "storage_locations", start)
	return err
}

// UpdateStorageLocationLastDownloaded updates the last downloaded timestamp.
func (d *DB) UpdateStorageLocationLastDownloaded(ctx context.Context, id string, timestamp int64) error {
	query := fmt.Sprintf(`UPDATE storage_locations SET %s = ? WHERE id = ?`, d.col("lastDownloadedAt"))
	start := time.Now()
	_, err := d.ExecContext(ctx, d.Rebind(query), timestamp, id)
	d.recordDBQuery(ctx, "storage_locations", start)
	return err
}

// DeleteStorageLocation deletes a storage location.
func (d *DB) DeleteStorageLocation(ctx context.Context, id string) error {
	query := `DELETE FROM storage_locations WHERE id = ?`
	start := time.Now()
	_, err := d.ExecContext(ctx, d.Rebind(query), id)
	d.recordDBQuery(ctx, "storage_locations", start)
	return err
}

// CreateCacheEntry creates a new cache entry.
func (d *DB) CreateCacheEntry(ctx context.Context, entry *CacheEntry) error {
	query := fmt.Sprintf(`INSERT INTO cache_entries (id, %s, version, %s, %s) VALUES (?, ?, ?, ?, ?)`,
		d.keyCol(), d.col("updatedAt"), d.col("locationId"))
	start := time.Now()
	_, err := d.ExecContext(ctx, d.Rebind(query), entry.ID, entry.Key, entry.Version, entry.UpdatedAt, entry.LocationID)
	d.recordDBQuery(ctx, "cache_entries", start)
	return err
}

// GetCacheEntry retrieves a cache entry by ID.
func (d *DB) GetCacheEntry(ctx context.Context, id string) (*CacheEntry, error) {
	var entry CacheEntry
	query := fmt.Sprintf(`SELECT id, %s, version, %s, %s FROM cache_entries WHERE id = ?`,
		d.keyCol(), d.col("updatedAt"), d.col("locationId"))
	start := time.Now()
	err := d.GetContext(ctx, &entry, d.Rebind(query), id)
	d.recordDBQuery(ctx, "cache_entries", start)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &entry, err
}

// GetCacheEntryByKeyVersion retrieves a cache entry by key and version.
func (d *DB) GetCacheEntryByKeyVersion(ctx context.Context, key, version string) (*CacheEntry, error) {
	var entry CacheEntry
	query := fmt.Sprintf(`SELECT id, %[1]s, version, %[2]s, %[3]s FROM cache_entries WHERE %[1]s = ? AND version = ?`,
		d.keyCol(), d.col("updatedAt"), d.col("locationId"))
	start := time.Now()
	err := d.GetContext(ctx, &entry, d.Rebind(query), key, version)
	d.recordDBQuery(ctx, "cache_entries", start)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &entry, err
}

// GetCacheEntryByKeyVersionPrefix retrieves the most recent cache entry matching a key prefix.
func (d *DB) GetCacheEntryByKeyVersionPrefix(ctx context.Context, keyPrefix, version string) (*CacheEntry, error) {
	var entry CacheEntry
	query := fmt.Sprintf(`SELECT id, %[1]s, version, %[2]s, %[3]s FROM cache_entries WHERE %[1]s LIKE ? AND version = ? ORDER BY %[2]s DESC LIMIT 1`,
		d.keyCol(), d.col("updatedAt"), d.col("locationId"))
	start := time.Now()
	err := d.GetContext(ctx, &entry, d.Rebind(query), keyPrefix+"%", version)
	d.recordDBQuery(ctx, "cache_entries", start)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &entry, err
}

// DeleteCacheEntry deletes a cache entry.
func (d *DB) DeleteCacheEntry(ctx context.Context, id string) error {
	query := `DELETE FROM cache_entries WHERE id = ?`
	start := time.Now()
	_, err := d.ExecContext(ctx, d.Rebind(query), id)
	d.recordDBQuery(ctx, "cache_entries", start)
	return err
}

// CompleteUploadResult contains the result of completing an upload.
type CompleteUploadResult struct {
	OldFolderName string // Non-empty if an existing cache entry was overwritten
}

// CompleteUpload atomically converts an upload to a cache entry.
// If a cache entry with the same key/version already exists, it will be overwritten.
// Returns the old folder name if an existing entry was overwritten (for storage cleanup).
func (d *DB) CompleteUpload(ctx context.Context, uploadID int64, cacheEntry *CacheEntry, storageLocation *StorageLocation) (*CompleteUploadResult, error) {
	tx, err := d.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	result := &CompleteUploadResult{}

	// Delete the upload
	deleteUploadQuery := `DELETE FROM uploads WHERE id = ?`
	start := time.Now()
	if _, err := tx.ExecContext(ctx, d.Rebind(deleteUploadQuery), uploadID); err != nil {
		return nil, err
	}
	d.recordDBQuery(ctx, "uploads", start)

	// Create storage location
	createLocQuery := fmt.Sprintf(`INSERT INTO storage_locations (id, %s, %s) VALUES (?, ?, ?)`,
		d.col("folderName"), d.col("partCount"))
	start = time.Now()
	if _, err := tx.ExecContext(ctx, d.Rebind(createLocQuery), storageLocation.ID, storageLocation.FolderName, storageLocation.PartCount); err != nil {
		return nil, err
	}
	d.recordDBQuery(ctx, "storage_locations", start)

	// Check for existing cache entry with same key/version
	var existingEntry CacheEntry
	checkExistingQuery := fmt.Sprintf(`SELECT id, %s, version, %s, %s FROM cache_entries WHERE %s = ? AND version = ?`,
		d.keyCol(), d.col("updatedAt"), d.col("locationId"), d.keyCol())
	start = time.Now()
	err = tx.GetContext(ctx, &existingEntry, d.Rebind(checkExistingQuery), cacheEntry.Key, cacheEntry.Version)
	d.recordDBQuery(ctx, "cache_entries", start)

	if err == nil {
		// Existing entry found - get the old location's folder name for cleanup
		var oldLocation StorageLocation
		getOldLocQuery := fmt.Sprintf(`SELECT id, %s, %s, %s, %s, %s, %s FROM storage_locations WHERE id = ?`,
			d.col("folderName"), d.col("partCount"), d.col("mergeStartedAt"),
			d.col("mergedAt"), d.col("partsDeletedAt"), d.col("lastDownloadedAt"))
		start = time.Now()
		if err := tx.GetContext(ctx, &oldLocation, d.Rebind(getOldLocQuery), existingEntry.LocationID); err == nil {
			d.recordDBQuery(ctx, "storage_locations", start)
			result.OldFolderName = oldLocation.FolderName
		} else {
			d.recordDBQuery(ctx, "storage_locations", start)
		}

		// Update the existing cache entry with new location
		updateEntryQuery := fmt.Sprintf(`UPDATE cache_entries SET %s = ?, %s = ? WHERE id = ?`,
			d.col("locationId"), d.col("updatedAt"))
		start = time.Now()
		if _, err := tx.ExecContext(ctx, d.Rebind(updateEntryQuery), storageLocation.ID, cacheEntry.UpdatedAt, existingEntry.ID); err != nil {
			return nil, err
		}
		d.recordDBQuery(ctx, "cache_entries", start)

		// Delete the old storage location after the cache entry points to the new one.
		deleteOldLocQuery := `DELETE FROM storage_locations WHERE id = ?`
		start = time.Now()
		if _, err := tx.ExecContext(ctx, d.Rebind(deleteOldLocQuery), existingEntry.LocationID); err != nil {
			return nil, err
		}
		d.recordDBQuery(ctx, "storage_locations", start)
	} else if err == sql.ErrNoRows {
		// No existing entry - create new cache entry
		createEntryQuery := fmt.Sprintf(`INSERT INTO cache_entries (id, %s, version, %s, %s) VALUES (?, ?, ?, ?, ?)`,
			d.keyCol(), d.col("updatedAt"), d.col("locationId"))
		start = time.Now()
		if _, err := tx.ExecContext(ctx, d.Rebind(createEntryQuery), cacheEntry.ID, cacheEntry.Key, cacheEntry.Version, cacheEntry.UpdatedAt, cacheEntry.LocationID); err != nil {
			return nil, err
		}
		d.recordDBQuery(ctx, "cache_entries", start)
	} else {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return result, nil
}

// GetStaleUploads retrieves uploads that haven't been updated recently.
func (d *DB) GetStaleUploads(ctx context.Context, olderThan int64, limit int) ([]*Upload, error) {
	var uploads []*Upload
	query := fmt.Sprintf(`SELECT id, %s, version, %s, %s, %s, %s, %s FROM uploads
			WHERE %s < ? AND (%s IS NULL OR %s < ?)
			LIMIT ?`,
		d.keyCol(), d.col("folderName"), d.col("createdAt"), d.col("lastPartUploadedAt"), d.col("uploadedBytes"), d.col("uploadedParts"),
		d.col("createdAt"), d.col("lastPartUploadedAt"), d.col("lastPartUploadedAt"))
	start := time.Now()
	err := d.SelectContext(ctx, &uploads, d.Rebind(query), olderThan, olderThan, limit)
	d.recordDBQuery(ctx, "uploads", start)
	return uploads, err
}

// GetMergedStorageLocationsForPartsCleanup retrieves storage locations ready for parts cleanup.
func (d *DB) GetMergedStorageLocationsForPartsCleanup(ctx context.Context, limit int) ([]*StorageLocation, error) {
	var locs []*StorageLocation
	query := fmt.Sprintf(`SELECT id, %s, %s, %s, %s, %s, %s
		FROM storage_locations WHERE %s IS NOT NULL AND %s IS NULL LIMIT ?`,
		d.col("folderName"), d.col("partCount"), d.col("mergeStartedAt"),
		d.col("mergedAt"), d.col("partsDeletedAt"), d.col("lastDownloadedAt"),
		d.col("mergedAt"), d.col("partsDeletedAt"))
	start := time.Now()
	err := d.SelectContext(ctx, &locs, d.Rebind(query), limit)
	d.recordDBQuery(ctx, "storage_locations", start)
	return locs, err
}

// GetStaleMerges retrieves storage locations with stalled merges.
func (d *DB) GetStaleMerges(ctx context.Context, olderThan int64) (int64, error) {
	query := fmt.Sprintf(`UPDATE storage_locations SET %s = NULL, %s = NULL
		WHERE %s IS NOT NULL AND %s IS NULL AND %s < ?`,
		d.col("mergeStartedAt"), d.col("mergedAt"),
		d.col("mergeStartedAt"), d.col("mergedAt"), d.col("mergeStartedAt"))
	start := time.Now()
	result, err := d.ExecContext(ctx, d.Rebind(query), olderThan)
	d.recordDBQuery(ctx, "storage_locations", start)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// GetOldCacheEntries retrieves cache entries not downloaded recently.
func (d *DB) GetOldCacheEntries(ctx context.Context, olderThan int64, limit int) ([]*StorageLocation, error) {
	var locs []*StorageLocation
	query := fmt.Sprintf(`SELECT sl.id, sl.%[1]s, sl.%[2]s, sl.%[3]s, sl.%[4]s, sl.%[5]s, sl.%[6]s
		FROM storage_locations sl
		INNER JOIN cache_entries ce ON ce.%[7]s = sl.id
		WHERE sl.%[6]s IS NOT NULL AND sl.%[6]s < ?
		LIMIT ?`,
		d.col("folderName"), d.col("partCount"), d.col("mergeStartedAt"),
		d.col("mergedAt"), d.col("partsDeletedAt"), d.col("lastDownloadedAt"),
		d.col("locationId"))
	start := time.Now()
	err := d.SelectContext(ctx, &locs, d.Rebind(query), olderThan, limit)
	d.recordDBQuery(ctx, "storage_locations", start)
	return locs, err
}

// GetOrphanedStorageLocations retrieves storage locations without cache entries.
func (d *DB) GetOrphanedStorageLocations(ctx context.Context, limit int) ([]*StorageLocation, error) {
	var locs []*StorageLocation
	query := fmt.Sprintf(`SELECT sl.id, sl.%[1]s, sl.%[2]s, sl.%[3]s, sl.%[4]s, sl.%[5]s, sl.%[6]s
		FROM storage_locations sl
		LEFT JOIN cache_entries ce ON ce.%[7]s = sl.id
		WHERE ce.id IS NULL
		LIMIT ?`,
		d.col("folderName"), d.col("partCount"), d.col("mergeStartedAt"),
		d.col("mergedAt"), d.col("partsDeletedAt"), d.col("lastDownloadedAt"),
		d.col("locationId"))
	start := time.Now()
	err := d.SelectContext(ctx, &locs, d.Rebind(query), limit)
	d.recordDBQuery(ctx, "storage_locations", start)
	return locs, err
}

// DeleteCacheEntriesByLocationID deletes cache entries by location ID.
func (d *DB) DeleteCacheEntriesByLocationID(ctx context.Context, locationID string) error {
	query := fmt.Sprintf(`DELETE FROM cache_entries WHERE %s = ?`, d.col("locationId"))
	start := time.Now()
	_, err := d.ExecContext(ctx, d.Rebind(query), locationID)
	d.recordDBQuery(ctx, "cache_entries", start)
	return err
}

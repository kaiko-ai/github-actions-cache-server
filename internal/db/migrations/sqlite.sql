CREATE TABLE IF NOT EXISTS storage_locations (
  id TEXT PRIMARY KEY,
  folderName TEXT NOT NULL,
  partCount INTEGER NOT NULL,
  mergeStartedAt INTEGER,
  mergedAt INTEGER,
  partsDeletedAt INTEGER,
  lastDownloadedAt INTEGER
);

CREATE TABLE IF NOT EXISTS cache_entries (
  id TEXT PRIMARY KEY,
  key TEXT NOT NULL,
  version TEXT NOT NULL,
  updatedAt INTEGER NOT NULL,
  locationId TEXT REFERENCES storage_locations(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_cache_entries_key_version ON cache_entries(key, version);

CREATE TABLE IF NOT EXISTS uploads (
  id INTEGER PRIMARY KEY,
  key TEXT NOT NULL,
  version TEXT NOT NULL,
  folderName TEXT NOT NULL,
  createdAt INTEGER NOT NULL,
  lastPartUploadedAt INTEGER,
  uploadedBytes INTEGER NOT NULL DEFAULT 0,
  uploadedParts INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_uploads_key_version ON uploads(key, version);

ALTER TABLE uploads ADD COLUMN uploadedBytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE uploads ADD COLUMN uploadedParts INTEGER NOT NULL DEFAULT 0;

ALTER TABLE storage_locations ADD COLUMN sizeBytes INTEGER NOT NULL DEFAULT 0;

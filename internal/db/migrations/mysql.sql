CREATE TABLE IF NOT EXISTS storage_locations (
  id VARCHAR(255) PRIMARY KEY,
  folderName TEXT NOT NULL,
  partCount INTEGER NOT NULL,
  mergeStartedAt BIGINT,
  mergedAt BIGINT,
  partsDeletedAt BIGINT,
  lastDownloadedAt BIGINT
);

CREATE TABLE IF NOT EXISTS cache_entries (
  id VARCHAR(255) PRIMARY KEY,
  `key` VARCHAR(512) NOT NULL,
  version VARCHAR(255) NOT NULL,
  updatedAt BIGINT NOT NULL,
  locationId VARCHAR(255),
  FOREIGN KEY (locationId) REFERENCES storage_locations(id) ON DELETE CASCADE
);

CREATE INDEX idx_cache_entries_key_version ON cache_entries(`key`, version);

CREATE TABLE IF NOT EXISTS uploads (
  id BIGINT PRIMARY KEY,
  `key` VARCHAR(512) NOT NULL,
  version VARCHAR(255) NOT NULL,
  folderName TEXT NOT NULL,
  createdAt BIGINT NOT NULL,
  lastPartUploadedAt BIGINT
);

CREATE INDEX idx_uploads_key_version ON uploads(`key`, version)

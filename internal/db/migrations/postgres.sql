CREATE TABLE IF NOT EXISTS storage_locations (
  id TEXT PRIMARY KEY,
  "folderName" TEXT NOT NULL,
  "partCount" INTEGER NOT NULL,
  "mergeStartedAt" BIGINT,
  "mergedAt" BIGINT,
  "partsDeletedAt" BIGINT,
  "lastDownloadedAt" BIGINT
);

CREATE TABLE IF NOT EXISTS cache_entries (
  id TEXT PRIMARY KEY,
  key TEXT NOT NULL,
  version TEXT NOT NULL,
  "updatedAt" BIGINT NOT NULL,
  "locationId" TEXT REFERENCES storage_locations(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_cache_entries_key_version ON cache_entries(key, version);

CREATE TABLE IF NOT EXISTS uploads (
  id BIGINT PRIMARY KEY,
  key TEXT NOT NULL,
  version TEXT NOT NULL,
  "folderName" TEXT NOT NULL,
  "createdAt" BIGINT NOT NULL,
  "lastPartUploadedAt" BIGINT
);

CREATE INDEX IF NOT EXISTS idx_uploads_key_version ON uploads(key, version)

// Package handlers provides HTTP handlers for the cache server.
package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/falcosecurity/github-actions-cache-server/internal/config"
	"github.com/falcosecurity/github-actions-cache-server/internal/db"
	"github.com/falcosecurity/github-actions-cache-server/internal/metrics"
	"github.com/falcosecurity/github-actions-cache-server/internal/storage"
)

const (
	signedURLExpiration   = 10 * time.Minute
	githubResultsReceiver = "https://results-receiver.actions.githubusercontent.com"
)

// Handler holds dependencies for HTTP handlers.
type Handler struct {
	config  *config.Config
	db      *db.DB
	storage storage.Adapter
	logger  *slog.Logger
}

// New creates a new Handler.
func New(cfg *config.Config, database *db.DB, storageAdapter storage.Adapter, logger *slog.Logger) *Handler {
	return &Handler{
		config:  cfg,
		db:      database,
		storage: storageAdapter,
		logger:  logger,
	}
}

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// requestLogger is a middleware that logs incoming HTTP requests.
func (h *Handler) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		h.logger.Info("request", "method", r.Method, "path", r.URL.Path, "status", rw.status)
	})
}

// Router returns a configured chi router with all routes.
func (h *Handler) Router() http.Handler {
	r := chi.NewRouter()

	// Add request logging middleware
	r.Use(h.requestLogger)

	// Utility routes
	r.Get("/", h.handleRoot)
	r.Get("/health", h.handleHealth)
	r.Get("/metrics", h.handleMetrics)

	// Twirp RPC routes
	r.Post("/twirp/github.actions.results.api.v1.CacheService/CreateCacheEntry", h.handleCreateCacheEntry)
	r.Post("/twirp/github.actions.results.api.v1.CacheService/GetCacheEntryDownloadURL", h.handleGetCacheEntryDownloadURL)
	r.Post("/twirp/github.actions.results.api.v1.CacheService/FinalizeCacheEntryUpload", h.handleFinalizeCacheEntryUpload)

	// Upload/Download routes
	r.Put("/upload/{uploadId}", h.handleUpload)
	r.Get("/download/{cacheEntryId}", h.handleDownload)

	// Catch-all proxy to GitHub results receiver
	r.HandleFunc("/*", h.handleCatchAllProxy)

	return r
}

// handleRoot returns OK.
func (h *Handler) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// handleHealth returns health status.
func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("healthy"))
}

// handleMetrics returns Prometheus-formatted metrics.
func (h *Handler) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !h.config.MetricsEnabled {
		h.recordError(ctx, r.URL.Path, "disabled", http.StatusNotFound)
		h.writeJSONError(w, http.StatusNotFound, "Metrics endpoint is disabled")
		return
	}

	handler := metrics.PrometheusHandler()
	if handler != nil {
		handler.ServeHTTP(w, r)
	} else {
		h.recordError(ctx, r.URL.Path, "not_initialized", http.StatusServiceUnavailable)
		h.writeJSONError(w, http.StatusServiceUnavailable, "Metrics not initialized")
	}
}

// CreateCacheEntry request/response types.
type CreateCacheEntryRequest struct {
	Key     *string `json:"key"`
	Version *string `json:"version"`
}

type CreateCacheEntryResponse struct {
	OK              bool   `json:"ok"`
	SignedUploadURL string `json:"signed_upload_url,omitempty"`
}

func (h *Handler) handleCreateCacheEntry(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req CreateCacheEntryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.recordError(ctx, r.URL.Path, "invalid_request", http.StatusBadRequest)
		h.writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if !h.validateKeyVersion(w, r, req.Key, req.Version) {
		return
	}

	key := *req.Key
	version := *req.Version

	// Check if there's an existing upload (don't check for existing cache entry - allow overwrite)
	existingUpload, err := h.db.GetUploadByKeyVersion(ctx, key, version)
	if err != nil {
		h.logger.Error("failed to check existing upload", "error", err)
		h.recordError(ctx, r.URL.Path, "database_error", http.StatusInternalServerError)
		h.writeJSONError(w, http.StatusInternalServerError, "database error")
		return
	}

	if existingUpload != nil {
		// Upload already in progress, return ok: false
		h.writeJSON(w, http.StatusOK, CreateCacheEntryResponse{OK: false})
		return
	}

	// Create new upload
	uploadID := generateNumberID()
	now := time.Now().UnixMilli()

	upload := &db.Upload{
		ID:         uploadID,
		Key:        key,
		Version:    version,
		FolderName: strconv.FormatInt(uploadID, 10),
		CreatedAt:  now,
	}

	if err := h.db.CreateUpload(ctx, upload); err != nil {
		h.logger.Error("failed to create upload", "error", err)
		h.recordError(ctx, r.URL.Path, "database_error", http.StatusInternalServerError)
		h.writeJSONError(w, http.StatusInternalServerError, "database error")
		return
	}

	signedUploadURL := fmt.Sprintf("%s/upload/%d", h.config.APIBaseURL, uploadID)

	if m := metrics.Get(); m != nil {
		m.RecordCacheOperation(ctx, "create", "success")
	}

	h.writeJSON(w, http.StatusOK, CreateCacheEntryResponse{
		OK:              true,
		SignedUploadURL: signedUploadURL,
	})
}

// GetCacheEntryDownloadURL request/response types.
type GetCacheEntryDownloadURLRequest struct {
	Key         *string             `json:"key"`
	RestoreKeys nullableStringSlice `json:"restore_keys"`
	Version     *string             `json:"version"`
}

type GetCacheEntryDownloadURLResponse struct {
	OK                bool   `json:"ok"`
	SignedDownloadURL string `json:"signed_download_url,omitempty"`
	MatchedKey        string `json:"matched_key,omitempty"`
}

func (h *Handler) handleGetCacheEntryDownloadURL(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req GetCacheEntryDownloadURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.recordError(ctx, r.URL.Path, "invalid_request", http.StatusBadRequest)
		h.writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if !h.validateKeyVersion(w, r, req.Key, req.Version) {
		return
	}

	key := *req.Key
	version := *req.Version

	// Build list of keys to search (primary key + restore keys)
	keys := []string{key}
	if len(req.RestoreKeys) > 0 {
		keys = append(keys, req.RestoreKeys...)
	}

	// Search for cache entry
	entry, err := h.findCacheEntry(ctx, keys, version)
	if err != nil {
		h.logger.Error("failed to find cache entry", "error", err)
		h.recordError(ctx, r.URL.Path, "database_error", http.StatusInternalServerError)
		h.writeJSONError(w, http.StatusInternalServerError, "database error")
		return
	}

	if entry == nil {
		if m := metrics.Get(); m != nil {
			m.RecordCacheOperation(ctx, "lookup", "miss")
		}
		h.writeJSON(w, http.StatusOK, GetCacheEntryDownloadURLResponse{OK: false})
		return
	}

	// Generate download URL
	var signedDownloadURL string
	if h.config.EnableDirectDownloads {
		// Try to get a signed URL from storage
		loc, err := h.db.GetStorageLocation(ctx, entry.LocationID)
		if err != nil {
			h.logger.Error("failed to get storage location", "error", err)
			h.recordError(ctx, r.URL.Path, "database_error", http.StatusInternalServerError)
			h.writeJSONError(w, http.StatusInternalServerError, "database error")
			return
		}

		if loc != nil && loc.MergedAt.Valid {
			// File is merged, can create signed URL
			objectName := fmt.Sprintf("%s/merged", loc.FolderName)
			url, err := h.storage.CreateDownloadURL(ctx, objectName, signedURLExpiration)
			if err == nil && url != "" {
				signedDownloadURL = url
			}
		}
	}

	// Fall back to internal download URL
	if signedDownloadURL == "" {
		signedDownloadURL = fmt.Sprintf("%s/download/%s", h.config.APIBaseURL, entry.ID)
	}

	if m := metrics.Get(); m != nil {
		m.RecordCacheOperation(ctx, "lookup", "hit")
	}

	h.writeJSON(w, http.StatusOK, GetCacheEntryDownloadURLResponse{
		OK:                true,
		SignedDownloadURL: signedDownloadURL,
		MatchedKey:        entry.Key,
	})
}

// findCacheEntry searches for a cache entry using the GitHub Actions cache matching logic.
func (h *Handler) findCacheEntry(ctx context.Context, keys []string, version string) (*db.CacheEntry, error) {
	for _, key := range keys {
		// Try exact match first
		entry, err := h.db.GetCacheEntryByKeyVersion(ctx, key, version)
		if err != nil {
			return nil, err
		}
		if entry != nil {
			return entry, nil
		}

		// Try prefix match
		entry, err = h.db.GetCacheEntryByKeyVersionPrefix(ctx, key, version)
		if err != nil {
			return nil, err
		}
		if entry != nil {
			return entry, nil
		}
	}

	return nil, nil
}

// FinalizeCacheEntryUpload request/response types.
type FinalizeCacheEntryUploadRequest struct {
	Key       *string     `json:"key"`
	Version   *string     `json:"version"`
	SizeBytes json.Number `json:"size_bytes,omitempty"`
}

type FinalizeCacheEntryUploadResponse struct {
	OK      bool   `json:"ok"`
	EntryID string `json:"entry_id,omitempty"`
}

func (h *Handler) handleFinalizeCacheEntryUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req FinalizeCacheEntryUploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.recordError(ctx, r.URL.Path, "invalid_request", http.StatusBadRequest)
		h.writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if !h.validateKeyVersion(w, r, req.Key, req.Version) {
		return
	}

	key := *req.Key
	version := *req.Version

	// Find the upload
	upload, err := h.db.GetUploadByKeyVersion(ctx, key, version)
	if err != nil {
		h.logger.Error("failed to get upload", "error", err)
		h.recordError(ctx, r.URL.Path, "database_error", http.StatusInternalServerError)
		h.writeJSONError(w, http.StatusInternalServerError, "database error")
		return
	}

	if upload == nil {
		h.recordError(ctx, r.URL.Path, "not_found", http.StatusNotFound)
		h.writeJSONError(w, http.StatusNotFound, "Upload not found")
		return
	}

	partsFolder := fmt.Sprintf("%s/parts", upload.FolderName)

	// Parse expected size from request and wait for all bytes to arrive
	var expectedSize int64
	if req.SizeBytes != "" {
		expectedSize, _ = req.SizeBytes.Int64()
	}

	if expectedSize > 0 {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			actualSize, err := h.storage.GetFolderSize(ctx, partsFolder)
			if err == nil && actualSize >= expectedSize {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
	}

	// Count uploaded parts
	partCount, err := h.storage.CountFilesInFolder(ctx, partsFolder)
	if err != nil {
		h.logger.Error("failed to count parts", "error", err)
		h.recordError(ctx, r.URL.Path, "storage_error", http.StatusInternalServerError)
		h.writeJSONError(w, http.StatusInternalServerError, "storage error")
		return
	}
	if partCount == 0 {
		h.recordError(ctx, r.URL.Path, "no_parts", http.StatusInternalServerError)
		h.writeJSONError(w, http.StatusInternalServerError, "No parts found for upload")
		return
	}

	// Create storage location and cache entry
	now := time.Now().UnixMilli()
	locationID := uuid.New().String()
	entryID := uuid.New().String()

	storageLocation := &db.StorageLocation{
		ID:         locationID,
		FolderName: upload.FolderName,
		PartCount:  partCount,
	}

	cacheEntry := &db.CacheEntry{
		ID:         entryID,
		Key:        key,
		Version:    version,
		UpdatedAt:  now,
		LocationID: locationID,
	}

	result, err := h.db.CompleteUpload(ctx, upload.ID, cacheEntry, storageLocation)
	if err != nil {
		h.logger.Error("failed to complete upload", "error", err)
		h.recordError(ctx, r.URL.Path, "database_error", http.StatusInternalServerError)
		h.writeJSONError(w, http.StatusInternalServerError, "database error")
		return
	}

	// If an existing entry was overwritten, delete the old storage folder
	if result.OldFolderName != "" {
		go func() {
			if err := h.storage.DeleteFolder(context.Background(), result.OldFolderName); err != nil {
				h.logger.Error("failed to delete old storage folder", "error", err, "folder", result.OldFolderName)
			}
		}()
	}

	// Return upload.ID as the entry_id (not the new UUID)
	h.writeJSON(w, http.StatusOK, FinalizeCacheEntryUploadResponse{
		OK:      true,
		EntryID: strconv.FormatInt(upload.ID, 10),
	})
}

// handleUpload handles chunked uploads.
func (h *Handler) handleUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	uploadIDStr := chi.URLParam(r, "uploadId")
	uploadID, err := strconv.ParseInt(uploadIDStr, 10, 64)
	if err != nil {
		h.recordError(ctx, r.URL.Path, "invalid_request", http.StatusBadRequest)
		h.writeJSONError(w, http.StatusBadRequest, "invalid upload ID")
		return
	}

	// Check for blocklist finalization (comp=blocklist)
	if r.URL.Query().Get("comp") == "blocklist" {
		w.Header().Set("x-ms-request-id", uuid.New().String())
		w.WriteHeader(http.StatusCreated)
		return
	}

	// Get the upload
	upload, err := h.db.GetUpload(ctx, uploadID)
	if err != nil {
		h.logger.Error("failed to get upload", "error", err)
		h.recordError(ctx, r.URL.Path, "database_error", http.StatusInternalServerError)
		h.writeJSONError(w, http.StatusInternalServerError, "database error")
		return
	}

	if upload == nil {
		// Unknown uploadId - silent return (no-op behavior matching TypeScript)
		w.Header().Set("x-ms-request-id", uuid.New().String())
		w.WriteHeader(http.StatusCreated)
		return
	}

	// Parse block ID to get chunk index
	blockIDBase64 := r.URL.Query().Get("blockid")
	chunkIndex := 0
	if blockIDBase64 != "" {
		index, err := getChunkIndexFromBlockID(blockIDBase64)
		if err != nil {
			// Invalid blockid - return 400 Bad Request
			h.recordError(ctx, r.URL.Path, "invalid_request", http.StatusBadRequest)
			h.writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("Invalid block id: %s", blockIDBase64))
			return
		}
		chunkIndex = index
	}

	// Upload the chunk
	objectName := fmt.Sprintf("%s/parts/%d", upload.FolderName, chunkIndex)

	start := time.Now()
	countingReader := &countingReader{r: r.Body}

	if err := h.storage.UploadStream(ctx, objectName, countingReader); err != nil {
		h.logger.Error("failed to upload chunk", "error", err)
		h.recordError(ctx, r.URL.Path, "storage_error", http.StatusInternalServerError)
		h.writeJSONError(w, http.StatusInternalServerError, "storage error")
		return
	}

	duration := time.Since(start)

	// Record metrics
	if m := metrics.Get(); m != nil {
		m.RecordStorageOperation(ctx, "uploadPart", h.config.StorageDriver, duration)
		m.RecordBytesUploaded(ctx, countingReader.count, "uploadPart", h.config.StorageDriver, "/upload/:uploadId")
	}

	// Update last part uploaded timestamp
	now := time.Now().UnixMilli()
	if err := h.db.UpdateUploadLastPart(ctx, uploadID, now); err != nil {
		h.logger.Error("failed to update upload timestamp", "error", err)
	}

	w.Header().Set("x-ms-request-id", uuid.New().String())
	w.WriteHeader(http.StatusCreated)
}

// handleDownload handles cache downloads with merge-on-demand.
func (h *Handler) handleDownload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	downloadStart := time.Now()

	cacheEntryID := chi.URLParam(r, "cacheEntryId")

	// Get cache entry
	entry, err := h.db.GetCacheEntry(ctx, cacheEntryID)
	if err != nil {
		h.logger.Error("failed to get cache entry", "error", err)
		h.recordError(ctx, r.URL.Path, "database_error", http.StatusInternalServerError)
		h.writeJSONError(w, http.StatusInternalServerError, "database error")
		return
	}

	if entry == nil {
		h.recordError(ctx, r.URL.Path, "not_found", http.StatusNotFound)
		h.writeJSONError(w, http.StatusNotFound, "cache entry not found")
		return
	}

	// Get storage location
	loc, err := h.db.GetStorageLocation(ctx, entry.LocationID)
	if err != nil {
		h.logger.Error("failed to get storage location", "error", err)
		h.recordError(ctx, r.URL.Path, "database_error", http.StatusInternalServerError)
		h.writeJSONError(w, http.StatusInternalServerError, "database error")
		return
	}

	if loc == nil {
		h.recordError(ctx, r.URL.Path, "not_found", http.StatusNotFound)
		h.writeJSONError(w, http.StatusNotFound, "storage location not found")
		return
	}

	// Update last downloaded timestamp
	now := time.Now().UnixMilli()
	if err := h.db.UpdateStorageLocationLastDownloaded(ctx, loc.ID, now); err != nil {
		h.logger.Error("failed to update last downloaded", "error", err)
	}

	var totalBytes int64

	// Check if already merged
	if loc.MergedAt.Valid {
		// Serve merged file
		reader, err := h.storage.CreateDownloadStream(ctx, fmt.Sprintf("%s/merged", loc.FolderName))
		if err != nil {
			h.logger.Error("failed to open merged file", "error", err)
			h.recordError(ctx, r.URL.Path, "storage_error", http.StatusInternalServerError)
			h.writeJSONError(w, http.StatusInternalServerError, "storage error")
			return
		}
		defer reader.Close()

		totalBytes, err = io.Copy(w, reader)
		if err != nil {
			h.logger.Error("failed to stream merged file", "error", err)
		}
	} else {
		// Need to merge parts
		// Start merge in background if not already started
		if !loc.MergeStartedAt.Valid {
			if err := h.db.UpdateStorageLocationMergeStarted(ctx, loc.ID, now); err != nil {
				h.logger.Error("failed to mark merge started", "error", err)
			}

			// Start background merge
			go h.mergeInBackground(loc.ID, loc.FolderName)
		}

		// Stream parts to response
		totalBytes, err = h.streamParts(ctx, w, loc.FolderName)
		if err != nil {
			h.logger.Error("failed to stream parts", "error", err)
		}
	}

	// Record metrics
	if m := metrics.Get(); m != nil {
		m.RecordStorageOperation(ctx, "download", h.config.StorageDriver, time.Since(downloadStart))
		m.RecordBytesDownloaded(ctx, totalBytes, "download", h.config.StorageDriver, "/download/:cacheEntryId")
	}
}

// streamParts streams all parts to a writer.
func (h *Handler) streamParts(ctx context.Context, w io.Writer, folderName string) (int64, error) {
	partsFolder := fmt.Sprintf("%s/parts", folderName)
	files, err := h.storage.ListFilesInFolder(ctx, partsFolder)
	if err != nil {
		return 0, fmt.Errorf("failed to list parts: %w", err)
	}

	// Parse and sort part indices numerically
	indices := make([]int, 0, len(files))
	for _, f := range files {
		idx, err := strconv.Atoi(f)
		if err != nil {
			h.logger.Warn("skipping non-numeric part file", "file", f)
			continue
		}
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	var totalBytes int64
	for _, idx := range indices {
		objectName := fmt.Sprintf("%s/parts/%d", folderName, idx)
		reader, err := h.storage.CreateDownloadStream(ctx, objectName)
		if err != nil {
			return totalBytes, fmt.Errorf("failed to open part %d: %w", idx, err)
		}

		n, err := io.Copy(w, reader)
		reader.Close()
		totalBytes += n

		if err != nil {
			return totalBytes, fmt.Errorf("failed to stream part %d: %w", idx, err)
		}
	}

	return totalBytes, nil
}

// mergeInBackground merges parts into a single file.
func (h *Handler) mergeInBackground(locationID, folderName string) {
	ctx := context.Background()
	mergeStart := time.Now()
	var bytesWritten int64

	// List part files
	partsFolder := fmt.Sprintf("%s/parts", folderName)
	files, err := h.storage.ListFilesInFolder(ctx, partsFolder)
	if err != nil {
		h.logger.Error("failed to list parts for merge", "error", err)
		h.recordMergeResult(ctx, "failure", mergeStart, 0)
		h.resetMergeState(ctx, locationID)
		return
	}

	// Parse and sort part indices numerically
	indices := make([]int, 0, len(files))
	for _, f := range files {
		idx, err := strconv.Atoi(f)
		if err != nil {
			h.logger.Warn("skipping non-numeric part file during merge", "file", f)
			continue
		}
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	// Create a pipe to stream merged content
	pr, pw := io.Pipe()

	// Write parts to pipe in goroutine and track bytes
	var pipeErr error
	go func() {
		defer pw.Close()
		for _, idx := range indices {
			objectName := fmt.Sprintf("%s/parts/%d", folderName, idx)
			reader, err := h.storage.CreateDownloadStream(ctx, objectName)
			if err != nil {
				h.logger.Error("failed to open part for merge", "error", err, "part", idx)
				pipeErr = err
				pw.CloseWithError(err)
				return
			}

			n, err := io.Copy(pw, reader)
			reader.Close()
			bytesWritten += n

			if err != nil {
				h.logger.Error("failed to copy part for merge", "error", err, "part", idx)
				pipeErr = err
				pw.CloseWithError(err)
				return
			}
		}
	}()

	// Upload merged file
	objectName := fmt.Sprintf("%s/merged", folderName)
	if err := h.storage.UploadStream(ctx, objectName, pr); err != nil {
		h.logger.Error("failed to upload merged file", "error", err)
		h.recordMergeResult(ctx, "failure", mergeStart, bytesWritten)
		h.resetMergeState(ctx, locationID)
		return
	}

	// Check if pipe had an error
	if pipeErr != nil {
		h.recordMergeResult(ctx, "failure", mergeStart, bytesWritten)
		h.resetMergeState(ctx, locationID)
		return
	}

	// Mark as merged
	now := time.Now().UnixMilli()
	if err := h.db.UpdateStorageLocationMerged(ctx, locationID, now); err != nil {
		h.logger.Error("failed to mark as merged", "error", err)
		h.recordMergeResult(ctx, "failure", mergeStart, bytesWritten)
		h.resetMergeState(ctx, locationID)
		return
	}

	// Delete parts folder and mark parts deleted
	if err := h.storage.DeleteFolder(ctx, partsFolder); err != nil {
		h.logger.Error("failed to delete parts folder", "error", err, "locationId", locationID)
		h.recordMergeResult(ctx, "failure", mergeStart, bytesWritten)
		h.resetMergeState(ctx, locationID)
		return
	}
	if err := h.db.UpdateStorageLocationPartsDeleted(ctx, locationID, now); err != nil {
		h.logger.Error("failed to mark parts deleted", "error", err, "locationId", locationID)
		h.recordMergeResult(ctx, "failure", mergeStart, bytesWritten)
		h.resetMergeState(ctx, locationID)
		return
	}

	h.recordMergeResult(ctx, "success", mergeStart, bytesWritten)
	h.logger.Info("merged cache entry", "locationId", locationID, "parts", len(indices), "bytes", bytesWritten)
}

// handleCatchAllProxy proxies unknown requests to GitHub results receiver.
func (h *Handler) handleCatchAllProxy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	targetURL, err := url.Parse(githubResultsReceiver)
	if err != nil {
		h.logger.Error("failed to parse proxy target URL", "error", err)
		h.recordError(ctx, r.URL.Path, "proxy_config_error", http.StatusInternalServerError)
		h.writeJSONError(w, http.StatusInternalServerError, "proxy configuration error")
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// Customize the director to preserve the original request path and query
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = targetURL.Host
	}

	// Handle errors
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		h.logger.Error("proxy error", "error", err, "path", r.URL.Path)
		h.recordError(r.Context(), r.URL.Path, "proxy_error", http.StatusBadGateway)
		h.writeJSONError(w, http.StatusBadGateway, "proxy error")
	}

	proxy.ServeHTTP(w, r)
}

// getChunkIndexFromBlockID parses the block ID to extract the chunk index.
func getChunkIndexFromBlockID(blockIDBase64 string) (int, error) {
	decoded, err := base64.StdEncoding.DecodeString(blockIDBase64)
	if err != nil {
		return 0, fmt.Errorf("failed to decode base64: %w", err)
	}

	// 64 bytes used by Docker buildx: read UInt32BE at offset 16
	if len(decoded) == 64 {
		return int(binary.BigEndian.Uint32(decoded[16:20])), nil
	}

	// 48 bytes used by everything else: UTF-8 string, slice off UUID (36 chars)
	if len(decoded) == 48 {
		str := string(decoded)
		// UUID is 36 chars, remainder is the index
		if len(str) > 36 {
			indexStr := str[36:]
			index, err := strconv.Atoi(strings.TrimSpace(indexStr))
			if err != nil {
				return 0, fmt.Errorf("failed to parse index from block ID: %w", err)
			}
			return index, nil
		}
	}

	return 0, fmt.Errorf("unsupported block ID length: %d", len(decoded))
}

// generateNumberID generates a random 10-digit number ID.
func generateNumberID() int64 {
	const digits = "0123456789"
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		// Fallback to time-based value if crypto/rand fails
		return time.Now().UnixNano() % 10000000000
	}
	for i := range b {
		b[i] = digits[int(b[i])%10]
	}
	// Ensure 10-digit number (no leading zero)
	if b[0] == '0' {
		b[0] = digits[(int(b[0])+1)%9+1]
	}
	n, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		return time.Now().UnixNano() % 10000000000
	}
	return n
}

type nullableStringSlice []string

func (n *nullableStringSlice) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*n = nil
		return nil
	}
	var v []string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*n = v
	return nil
}

func (h *Handler) validateKeyVersion(w http.ResponseWriter, r *http.Request, key, version *string) bool {
	if key == nil || version == nil {
		h.recordError(r.Context(), r.URL.Path, "invalid_request", http.StatusBadRequest)
		h.writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

func (h *Handler) resetMergeState(ctx context.Context, locationID string) {
	if err := h.db.ResetStorageLocationMergeState(ctx, locationID); err != nil {
		h.logger.Error("failed to reset merge state", "error", err, "locationId", locationID)
	}
}

// countingReader wraps a reader and counts bytes read.
type countingReader struct {
	r     io.Reader
	count int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.count += int64(n)
	return n, err
}

// writeJSON writes a JSON response.
func (h *Handler) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeJSONError writes a JSON error response and logs the error.
func (h *Handler) writeJSONError(w http.ResponseWriter, status int, message string) {
	h.logger.Warn("error response", "status", status, "message", message)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"statusCode":    status,
		"statusMessage": message,
	})
}

// recordError records an error metric.
func (h *Handler) recordError(ctx context.Context, endpoint, errorType string, statusCode int) {
	if m := metrics.Get(); m != nil {
		m.RecordError(ctx, endpoint, errorType, statusCode)
	}
}

// recordMergeResult records merge operation metrics.
func (h *Handler) recordMergeResult(ctx context.Context, status string, start time.Time, bytesWritten int64) {
	if m := metrics.Get(); m != nil {
		m.RecordMergeOperation(ctx, status, time.Since(start), bytesWritten)
	}
}

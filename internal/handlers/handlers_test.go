package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/falcosecurity/github-actions-cache-server/internal/config"
	"github.com/falcosecurity/github-actions-cache-server/internal/db"
	"github.com/falcosecurity/github-actions-cache-server/internal/storage"
)

func TestGetChunkIndexFromBlockID_64Byte(t *testing.T) {
	// Create a 64-byte block ID similar to Docker BuildX format
	// The chunk index is a UInt32BE at offset 16
	data := make([]byte, 64)
	binary.BigEndian.PutUint32(data[16:20], 42)
	blockID := base64.StdEncoding.EncodeToString(data)

	index, err := getChunkIndexFromBlockID(blockID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if index != 42 {
		t.Errorf("expected index 42, got %d", index)
	}
}

func TestGetChunkIndexFromBlockID_48Byte(t *testing.T) {
	// Create a 48-byte block ID with UUID prefix (36 chars) + index
	// UUID format: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx (36 chars)
	uuid := "12345678-1234-1234-1234-123456789012"
	indexStr := "000000000123" // padded to fill 48 bytes total
	data := uuid + indexStr
	if len(data) != 48 {
		t.Fatalf("test data length should be 48, got %d", len(data))
	}
	blockID := base64.StdEncoding.EncodeToString([]byte(data))

	index, err := getChunkIndexFromBlockID(blockID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if index != 123 {
		t.Errorf("expected index 123, got %d", index)
	}
}

func TestGetChunkIndexFromBlockID_48Byte_FirstChunk(t *testing.T) {
	// Test first chunk (index 0)
	uuid := "12345678-1234-1234-1234-123456789012"
	indexStr := "000000000000" // index 0
	data := uuid + indexStr
	blockID := base64.StdEncoding.EncodeToString([]byte(data))

	index, err := getChunkIndexFromBlockID(blockID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if index != 0 {
		t.Errorf("expected index 0, got %d", index)
	}
}

func TestGetChunkIndexFromBlockID_InvalidBase64(t *testing.T) {
	_, err := getChunkIndexFromBlockID("not-valid-base64!!!")
	if err == nil {
		t.Error("expected error for invalid base64")
	}
}

func TestGetChunkIndexFromBlockID_UnsupportedLength(t *testing.T) {
	// Create data with unsupported length (not 48 or 64)
	data := make([]byte, 32)
	blockID := base64.StdEncoding.EncodeToString(data)

	_, err := getChunkIndexFromBlockID(blockID)
	if err == nil {
		t.Error("expected error for unsupported length")
	}
}

func TestGenerateNumberID(t *testing.T) {
	// Test that generated IDs are within expected range
	for i := 0; i < 100; i++ {
		id := generateNumberID()
		if id < 1000000000 || id > 9999999999 {
			t.Errorf("ID %d is outside valid range [1000000000, 9999999999]", id)
		}
	}
}

func TestGenerateNumberID_Uniqueness(t *testing.T) {
	// Generate many IDs and check for duplicates
	ids := make(map[int64]bool)
	for i := 0; i < 1000; i++ {
		id := generateNumberID()
		if ids[id] {
			t.Errorf("duplicate ID generated: %d", id)
		}
		ids[id] = true
	}
}

func TestCountingReader(t *testing.T) {
	data := []byte("hello world")
	r := &countingReader{r: &mockReader{data: data}}

	buf := make([]byte, 5)
	n, _ := r.Read(buf)
	if n != 5 {
		t.Errorf("expected 5 bytes read, got %d", n)
	}
	if r.count != 5 {
		t.Errorf("expected count 5, got %d", r.count)
	}

	n, _ = r.Read(buf)
	if r.count != 10 {
		t.Errorf("expected count 10, got %d", r.count)
	}
}

type mockReader struct {
	data   []byte
	offset int
}

func (m *mockReader) Read(p []byte) (int, error) {
	if m.offset >= len(m.data) {
		return 0, nil
	}
	n := copy(p, m.data[m.offset:])
	m.offset += n
	return n, nil
}

// setupTestHandler creates a Handler with test dependencies (in-memory SQLite and temp directory storage).
func setupTestHandler(t *testing.T) *Handler {
	t.Helper()

	// Create temp directory for storage
	tmpDir, err := os.MkdirTemp("", "handler-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	// Create filesystem storage adapter
	storageAdapter, err := storage.NewFilesystemAdapter(tmpDir, 1024*1024)
	if err != nil {
		t.Fatalf("failed to create storage adapter: %v", err)
	}

	// Create in-memory SQLite database
	cfg := &config.Config{
		DBDriver:     "sqlite",
		DBSqlitePath: ":memory:",
	}
	database, err := db.New(cfg)
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	// Run migrations
	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	// Create handler
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return New(cfg, database, storageAdapter, logger)
}

// createTestUpload creates an upload record in the database.
func createTestUpload(t *testing.T, h *Handler, key, version, folderName string) *db.Upload {
	t.Helper()

	upload := &db.Upload{
		ID:         generateNumberID(),
		Key:        key,
		Version:    version,
		FolderName: folderName,
		CreatedAt:  time.Now().Unix(),
	}

	if err := h.db.CreateUpload(context.Background(), upload); err != nil {
		t.Fatalf("failed to create upload: %v", err)
	}

	return upload
}

// createTestPart creates a part file in storage.
func createTestPart(t *testing.T, h *Handler, folderName string, index int) {
	t.Helper()

	partName := folderName + "/parts/part" + string(rune('0'+index))
	data := []byte("test part data")
	if err := h.storage.UploadStream(context.Background(), partName, bytes.NewReader(data)); err != nil {
		t.Fatalf("failed to create test part: %v", err)
	}
}

func TestFinalizeCacheEntryUpload_StringSizeBytes(t *testing.T) {
	h := setupTestHandler(t)

	// Create an upload first
	upload := createTestUpload(t, h, "test-key", "test-version", "folder-string")

	// Create a test part file
	createTestPart(t, h, upload.FolderName, 0)

	// Test: Send finalize request with size_bytes as STRING (GitHub toolkit behavior)
	body := `{"key": "test-key", "version": "test-version", "size_bytes": "1048576"}`
	req := httptest.NewRequest(http.MethodPost, "/twirp/github.actions.results.api.v1.CacheService/FinalizeCacheEntryUpload", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	h.handleFinalizeCacheEntryUpload(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestFinalizeCacheEntryUpload_NumericSizeBytes(t *testing.T) {
	h := setupTestHandler(t)

	// Create an upload first
	upload := createTestUpload(t, h, "test-key", "test-version", "folder-numeric")

	// Create a test part file
	createTestPart(t, h, upload.FolderName, 0)

	// Test: Send finalize request with size_bytes as NUMBER
	body := `{"key": "test-key", "version": "test-version", "size_bytes": 1048576}`
	req := httptest.NewRequest(http.MethodPost, "/twirp/github.actions.results.api.v1.CacheService/FinalizeCacheEntryUpload", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	h.handleFinalizeCacheEntryUpload(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestFinalizeCacheEntryUpload_MissingSizeBytes(t *testing.T) {
	h := setupTestHandler(t)

	// Create an upload first
	upload := createTestUpload(t, h, "test-key", "test-version", "folder-missing")

	// Create a test part file
	createTestPart(t, h, upload.FolderName, 0)

	// Test: Send finalize request without size_bytes field
	body := `{"key": "test-key", "version": "test-version"}`
	req := httptest.NewRequest(http.MethodPost, "/twirp/github.actions.results.api.v1.CacheService/FinalizeCacheEntryUpload", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	h.handleFinalizeCacheEntryUpload(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestFinalizeCacheEntryUpload_UploadNotFound(t *testing.T) {
	h := setupTestHandler(t)

	// Test: Send finalize request for non-existent upload
	body := `{"key": "nonexistent-key", "version": "nonexistent-version"}`
	req := httptest.NewRequest(http.MethodPost, "/twirp/github.actions.results.api.v1.CacheService/FinalizeCacheEntryUpload", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	h.handleFinalizeCacheEntryUpload(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestResponseWriter_CapturesStatusCode(t *testing.T) {
	// Test that our responseWriter wrapper correctly captures status codes
	tests := []struct {
		name     string
		status   int
		expected int
	}{
		{"OK", http.StatusOK, http.StatusOK},
		{"BadRequest", http.StatusBadRequest, http.StatusBadRequest},
		{"NotFound", http.StatusNotFound, http.StatusNotFound},
		{"InternalServerError", http.StatusInternalServerError, http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			rw := &responseWriter{ResponseWriter: rr, status: http.StatusOK}

			rw.WriteHeader(tt.status)

			if rw.status != tt.expected {
				t.Errorf("expected status %d, got %d", tt.expected, rw.status)
			}
			if rr.Code != tt.expected {
				t.Errorf("expected underlying recorder status %d, got %d", tt.expected, rr.Code)
			}
		})
	}
}

func TestRequestLogger_LogsRequests(t *testing.T) {
	h := setupTestHandler(t)
	router := h.Router()

	// Test health endpoint (should return 200)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}

func TestRequestLogger_LogsErrorResponses(t *testing.T) {
	h := setupTestHandler(t)
	router := h.Router()

	// Test finalize with invalid body (should return 400 and log error)
	req := httptest.NewRequest(http.MethodPost, "/twirp/github.actions.results.api.v1.CacheService/FinalizeCacheEntryUpload", strings.NewReader("invalid json"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

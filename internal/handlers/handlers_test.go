package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
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
	body := `{"key": "test-key", "version": "test-version", "size_bytes": "14"}`
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
	body := `{"key": "test-key", "version": "test-version", "size_bytes": 14}`
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

// createTestPartNumeric creates a part file with a numeric name in storage.
func createTestPartNumeric(t *testing.T, h *Handler, folderName string, index int, data []byte) {
	t.Helper()

	partName := fmt.Sprintf("%s/parts/%d", folderName, index)
	if err := h.storage.UploadStream(context.Background(), partName, bytes.NewReader(data)); err != nil {
		t.Fatalf("failed to create test part: %v", err)
	}
}

func TestStreamParts_SequentialIndices(t *testing.T) {
	h := setupTestHandler(t)
	ctx := context.Background()
	folderName := "test-folder-seq"

	// Create parts with sequential 0-based indices
	part0 := []byte("part0-data")
	part1 := []byte("part1-data")
	part2 := []byte("part2-data")
	createTestPartNumeric(t, h, folderName, 0, part0)
	createTestPartNumeric(t, h, folderName, 1, part1)
	createTestPartNumeric(t, h, folderName, 2, part2)

	// Stream parts
	var buf bytes.Buffer
	totalBytes, err := h.streamParts(ctx, &buf, folderName)
	if err != nil {
		t.Fatalf("streamParts failed: %v", err)
	}

	expected := append(append(part0, part1...), part2...)
	if !bytes.Equal(buf.Bytes(), expected) {
		t.Errorf("streamed data doesn't match: expected %q, got %q", expected, buf.Bytes())
	}
	if totalBytes != int64(len(expected)) {
		t.Errorf("totalBytes doesn't match: expected %d, got %d", len(expected), totalBytes)
	}
}

func TestStreamParts_NonSequentialIndices(t *testing.T) {
	h := setupTestHandler(t)
	ctx := context.Background()
	folderName := "test-folder-nonseq"

	// Create parts with 1-based indices (not starting at 0)
	part1 := []byte("part1-data")
	part2 := []byte("part2-data")
	part3 := []byte("part3-data")
	createTestPartNumeric(t, h, folderName, 1, part1)
	createTestPartNumeric(t, h, folderName, 2, part2)
	createTestPartNumeric(t, h, folderName, 3, part3)

	// Stream parts
	var buf bytes.Buffer
	totalBytes, err := h.streamParts(ctx, &buf, folderName)
	if err != nil {
		t.Fatalf("streamParts failed: %v", err)
	}

	// Should stream in sorted order: 1, 2, 3
	expected := append(append(part1, part2...), part3...)
	if !bytes.Equal(buf.Bytes(), expected) {
		t.Errorf("streamed data doesn't match: expected %q, got %q", expected, buf.Bytes())
	}
	if totalBytes != int64(len(expected)) {
		t.Errorf("totalBytes doesn't match: expected %d, got %d", len(expected), totalBytes)
	}
}

func TestStreamParts_GapsInIndices(t *testing.T) {
	h := setupTestHandler(t)
	ctx := context.Background()
	folderName := "test-folder-gaps"

	// Create parts with gaps in indices (e.g., 0, 2, 5)
	part0 := []byte("part0-data")
	part2 := []byte("part2-data")
	part5 := []byte("part5-data")
	createTestPartNumeric(t, h, folderName, 0, part0)
	createTestPartNumeric(t, h, folderName, 2, part2)
	createTestPartNumeric(t, h, folderName, 5, part5)

	// Stream parts
	var buf bytes.Buffer
	totalBytes, err := h.streamParts(ctx, &buf, folderName)
	if err != nil {
		t.Fatalf("streamParts failed: %v", err)
	}

	// Should stream in sorted order: 0, 2, 5
	expected := append(append(part0, part2...), part5...)
	if !bytes.Equal(buf.Bytes(), expected) {
		t.Errorf("streamed data doesn't match: expected %q, got %q", expected, buf.Bytes())
	}
	if totalBytes != int64(len(expected)) {
		t.Errorf("totalBytes doesn't match: expected %d, got %d", len(expected), totalBytes)
	}
}

func TestStreamParts_CorrectOrder(t *testing.T) {
	h := setupTestHandler(t)
	ctx := context.Background()
	folderName := "test-folder-order"

	// Create parts in non-sorted order to verify sorting
	part10 := []byte("part10-data")
	part2 := []byte("part2-data")
	part1 := []byte("part1-data")
	createTestPartNumeric(t, h, folderName, 10, part10)
	createTestPartNumeric(t, h, folderName, 2, part2)
	createTestPartNumeric(t, h, folderName, 1, part1)

	// Stream parts
	var buf bytes.Buffer
	totalBytes, err := h.streamParts(ctx, &buf, folderName)
	if err != nil {
		t.Fatalf("streamParts failed: %v", err)
	}

	// Should stream in numeric sorted order: 1, 2, 10 (not lexicographic "1", "10", "2")
	expected := append(append(part1, part2...), part10...)
	if !bytes.Equal(buf.Bytes(), expected) {
		t.Errorf("streamed data doesn't match: expected %q, got %q", expected, buf.Bytes())
	}
	if totalBytes != int64(len(expected)) {
		t.Errorf("totalBytes doesn't match: expected %d, got %d", len(expected), totalBytes)
	}
}

func TestStreamParts_EmptyFolder(t *testing.T) {
	h := setupTestHandler(t)
	ctx := context.Background()
	folderName := "test-folder-empty"

	// Don't create any parts - folder doesn't exist

	// Stream parts
	var buf bytes.Buffer
	totalBytes, err := h.streamParts(ctx, &buf, folderName)
	if err != nil {
		t.Fatalf("streamParts failed: %v", err)
	}

	if buf.Len() != 0 {
		t.Errorf("expected empty buffer, got %d bytes", buf.Len())
	}
	if totalBytes != 0 {
		t.Errorf("expected 0 totalBytes, got %d", totalBytes)
	}
}

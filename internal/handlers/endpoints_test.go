package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/falcosecurity/github-actions-cache-server/internal/config"
	"github.com/falcosecurity/github-actions-cache-server/internal/db"
	"github.com/falcosecurity/github-actions-cache-server/internal/metrics"
	"github.com/falcosecurity/github-actions-cache-server/internal/storage"
)

type hookStorageAdapter struct {
	base            storage.Adapter
	uploadErr       error
	countErr        error
	listErr         error
	folderSizeErr   error
	downloadErr     error
	deleteFolderErr error
	downloadURL     string
	downloadURLErr  error
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (r roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return r(req)
}

func (h *hookStorageAdapter) CreateDownloadStream(ctx context.Context, objectName string) (io.ReadCloser, error) {
	if h.downloadErr != nil {
		return nil, h.downloadErr
	}
	return h.base.CreateDownloadStream(ctx, objectName)
}

func (h *hookStorageAdapter) UploadStream(ctx context.Context, objectName string, r io.Reader) error {
	if h.uploadErr != nil {
		return h.uploadErr
	}
	return h.base.UploadStream(ctx, objectName, r)
}

func (h *hookStorageAdapter) DeleteFolder(ctx context.Context, folderName string) error {
	if h.deleteFolderErr != nil {
		return h.deleteFolderErr
	}
	return h.base.DeleteFolder(ctx, folderName)
}

func (h *hookStorageAdapter) CountFilesInFolder(ctx context.Context, folderName string) (int, error) {
	if h.countErr != nil {
		return 0, h.countErr
	}
	return h.base.CountFilesInFolder(ctx, folderName)
}

func (h *hookStorageAdapter) ListFilesInFolder(ctx context.Context, folderName string) ([]string, error) {
	if h.listErr != nil {
		return nil, h.listErr
	}
	return h.base.ListFilesInFolder(ctx, folderName)
}

func (h *hookStorageAdapter) GetFolderSize(ctx context.Context, folderName string) (int64, error) {
	if h.folderSizeErr != nil {
		return 0, h.folderSizeErr
	}
	return h.base.GetFolderSize(ctx, folderName)
}

func (h *hookStorageAdapter) CreateDownloadURL(ctx context.Context, objectName string, expiry time.Duration) (string, error) {
	if h.downloadURLErr != nil {
		return "", h.downloadURLErr
	}
	if h.downloadURL != "" {
		return h.downloadURL, nil
	}
	return h.base.CreateDownloadURL(ctx, objectName, expiry)
}

func (h *hookStorageAdapter) Close() error {
	return h.base.Close()
}

func setupEndpointHandler(t *testing.T) *Handler {
	t.Helper()

	// Create temp directory for storage
	tmpDir, err := os.MkdirTemp("", "handler-endpoint-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	storageAdapter, err := storage.NewFilesystemAdapter(tmpDir, 1024*1024)
	if err != nil {
		t.Fatalf("failed to create storage adapter: %v", err)
	}

	cfg := &config.Config{
		APIBaseURL:           "http://cache.test",
		StorageDriver:        "filesystem",
		DBDriver:             "sqlite",
		DBSqlitePath:         ":memory:",
		MetricsEnabled:       false,
		StorageHighWaterMark: 1024 * 1024,
	}
	database, err := db.New(cfg)
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	t.Cleanup(func() { database.Close() })

	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	return New(cfg, database, storageAdapter, logger)
}

func makeBlockID64(index uint32) string {
	block := make([]byte, 64)
	binary.BigEndian.PutUint32(block[16:20], index)
	return base64.StdEncoding.EncodeToString(block)
}

func decodeJSONBody[T any](t *testing.T, rr *httptest.ResponseRecorder) T {
	t.Helper()

	var out T
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to decode JSON body %q: %v", rr.Body.String(), err)
	}
	return out
}

func createCacheEntryWithLocation(t *testing.T, h *Handler, key, version, folderName string, partCount int) (string, string) {
	t.Helper()

	locationID := uuid.New().String()
	entryID := uuid.New().String()
	now := time.Now().UnixMilli()

	loc := &db.StorageLocation{
		ID:         locationID,
		FolderName: folderName,
		PartCount:  partCount,
	}
	if err := h.db.CreateStorageLocation(context.Background(), loc); err != nil {
		t.Fatalf("failed to create storage location: %v", err)
	}

	entry := &db.CacheEntry{
		ID:         entryID,
		Key:        key,
		Version:    version,
		UpdatedAt:  now,
		LocationID: locationID,
	}
	if err := h.db.CreateCacheEntry(context.Background(), entry); err != nil {
		t.Fatalf("failed to create cache entry: %v", err)
	}

	return entryID, locationID
}

func TestRouter_UtilityEndpoints(t *testing.T) {
	h := setupEndpointHandler(t)
	router := h.Router()

	rootReq := httptest.NewRequest(http.MethodGet, "/", nil)
	rootRR := httptest.NewRecorder()
	router.ServeHTTP(rootRR, rootReq)
	if rootRR.Code != http.StatusOK {
		t.Fatalf("expected 200 for root, got %d", rootRR.Code)
	}
	if strings.TrimSpace(rootRR.Body.String()) != "OK" {
		t.Fatalf("unexpected root body: %q", rootRR.Body.String())
	}

	healthReq := httptest.NewRequest(http.MethodGet, "/health", nil)
	healthRR := httptest.NewRecorder()
	router.ServeHTTP(healthRR, healthReq)
	if healthRR.Code != http.StatusOK {
		t.Fatalf("expected 200 for health, got %d", healthRR.Code)
	}
	if strings.TrimSpace(healthRR.Body.String()) != "healthy" {
		t.Fatalf("unexpected health body: %q", healthRR.Body.String())
	}
}

func TestRouter_MetricsDisabled(t *testing.T) {
	h := setupEndpointHandler(t)
	h.config.MetricsEnabled = false

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	h.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestRouter_MetricsEnabled(t *testing.T) {
	h := setupEndpointHandler(t)
	h.config.MetricsEnabled = true

	shutdown, err := metrics.Init(context.Background(), metrics.Config{Enabled: true})
	if err != nil {
		t.Fatalf("failed to init metrics: %v", err)
	}
	t.Cleanup(func() {
		_ = shutdown(context.Background())
	})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	h.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestCreateCacheEntry_SuccessAndDuplicate(t *testing.T) {
	h := setupEndpointHandler(t)
	router := h.Router()

	body := `{"key":"linux-amd64","version":"v1"}`
	req := httptest.NewRequest(http.MethodPost, "/twirp/github.actions.results.api.v1.CacheService/CreateCacheEntry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}

	resp := decodeJSONBody[CreateCacheEntryResponse](t, rr)
	if !resp.OK {
		t.Fatalf("expected ok=true, got false")
	}
	if !strings.HasPrefix(resp.SignedUploadURL, "http://cache.test/upload/") {
		t.Fatalf("unexpected signed upload URL: %q", resp.SignedUploadURL)
	}

	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/twirp/github.actions.results.api.v1.CacheService/CreateCacheEntry", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr2.Code, rr2.Body.String())
	}
	resp2 := decodeJSONBody[CreateCacheEntryResponse](t, rr2)
	if resp2.OK {
		t.Fatalf("expected ok=false for duplicate in-progress upload")
	}
}

func TestCreateCacheEntry_InvalidRequestsAndDBError(t *testing.T) {
	h := setupEndpointHandler(t)
	router := h.Router()

	invalidReq := httptest.NewRequest(http.MethodPost, "/twirp/github.actions.results.api.v1.CacheService/CreateCacheEntry", strings.NewReader("{"))
	invalidRR := httptest.NewRecorder()
	router.ServeHTTP(invalidRR, invalidReq)
	if invalidRR.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", invalidRR.Code)
	}

	missingFieldReq := httptest.NewRequest(http.MethodPost, "/twirp/github.actions.results.api.v1.CacheService/CreateCacheEntry", strings.NewReader(`{"key":"k"}`))
	missingFieldReq.Header.Set("Content-Type", "application/json")
	missingFieldRR := httptest.NewRecorder()
	router.ServeHTTP(missingFieldRR, missingFieldReq)
	if missingFieldRR.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing version, got %d", missingFieldRR.Code)
	}

	if err := h.db.Close(); err != nil {
		t.Fatalf("failed to close db: %v", err)
	}

	dbErrorReq := httptest.NewRequest(http.MethodPost, "/twirp/github.actions.results.api.v1.CacheService/CreateCacheEntry", strings.NewReader(`{"key":"k","version":"v"}`))
	dbErrorReq.Header.Set("Content-Type", "application/json")
	dbErrorRR := httptest.NewRecorder()
	router.ServeHTTP(dbErrorRR, dbErrorReq)
	if dbErrorRR.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 after db close, got %d (%s)", dbErrorRR.Code, dbErrorRR.Body.String())
	}
}

func TestGetCacheEntryDownloadURL_MissAndMatches(t *testing.T) {
	h := setupEndpointHandler(t)
	router := h.Router()

	missReq := httptest.NewRequest(http.MethodPost, "/twirp/github.actions.results.api.v1.CacheService/GetCacheEntryDownloadURL", strings.NewReader(`{"key":"missing","version":"v1","restore_keys":null}`))
	missReq.Header.Set("Content-Type", "application/json")
	missRR := httptest.NewRecorder()
	router.ServeHTTP(missRR, missReq)
	if missRR.Code != http.StatusOK {
		t.Fatalf("expected 200 miss, got %d (%s)", missRR.Code, missRR.Body.String())
	}
	missResp := decodeJSONBody[GetCacheEntryDownloadURLResponse](t, missRR)
	if missResp.OK {
		t.Fatalf("expected miss response ok=false")
	}

	exactEntryID, _ := createCacheEntryWithLocation(t, h, "linux-go", "v1", "folder-exact", 1)
	exactReq := httptest.NewRequest(http.MethodPost, "/twirp/github.actions.results.api.v1.CacheService/GetCacheEntryDownloadURL", strings.NewReader(`{"key":"linux-go","version":"v1","restore_keys":[]}`))
	exactReq.Header.Set("Content-Type", "application/json")
	exactRR := httptest.NewRecorder()
	router.ServeHTTP(exactRR, exactReq)
	if exactRR.Code != http.StatusOK {
		t.Fatalf("expected 200 exact hit, got %d (%s)", exactRR.Code, exactRR.Body.String())
	}
	exactResp := decodeJSONBody[GetCacheEntryDownloadURLResponse](t, exactRR)
	if !exactResp.OK {
		t.Fatalf("expected exact hit response ok=true")
	}
	expectedFallback := fmt.Sprintf("http://cache.test/download/%s", exactEntryID)
	if exactResp.SignedDownloadURL != expectedFallback {
		t.Fatalf("expected fallback URL %q, got %q", expectedFallback, exactResp.SignedDownloadURL)
	}

	_, _ = createCacheEntryWithLocation(t, h, "linux-go-abc", "v2", "folder-prefix", 1)
	prefixReq := httptest.NewRequest(http.MethodPost, "/twirp/github.actions.results.api.v1.CacheService/GetCacheEntryDownloadURL", strings.NewReader(`{"key":"linux-node","version":"v2","restore_keys":["linux-go-"]}`))
	prefixReq.Header.Set("Content-Type", "application/json")
	prefixRR := httptest.NewRecorder()
	router.ServeHTTP(prefixRR, prefixReq)
	if prefixRR.Code != http.StatusOK {
		t.Fatalf("expected 200 prefix hit, got %d (%s)", prefixRR.Code, prefixRR.Body.String())
	}
	prefixResp := decodeJSONBody[GetCacheEntryDownloadURLResponse](t, prefixRR)
	if !prefixResp.OK {
		t.Fatalf("expected prefix hit response ok=true")
	}
	if prefixResp.MatchedKey != "linux-go-abc" {
		t.Fatalf("expected matched key linux-go-abc, got %q", prefixResp.MatchedKey)
	}
}

func TestGetCacheEntryDownloadURL_DirectDownloadSignedURL(t *testing.T) {
	h := setupEndpointHandler(t)
	h.config.EnableDirectDownloads = true
	h.storage = &hookStorageAdapter{
		base:        h.storage,
		downloadURL: "https://signed.example/cache.tar",
	}

	router := h.Router()
	entryID, locationID := createCacheEntryWithLocation(t, h, "linux-direct", "v1", "folder-direct", 1)
	_ = entryID
	if err := h.db.UpdateStorageLocationMerged(context.Background(), locationID, time.Now().UnixMilli()); err != nil {
		t.Fatalf("failed to mark merged: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/twirp/github.actions.results.api.v1.CacheService/GetCacheEntryDownloadURL", strings.NewReader(`{"key":"linux-direct","version":"v1","restore_keys":[]}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	resp := decodeJSONBody[GetCacheEntryDownloadURLResponse](t, rr)
	if resp.SignedDownloadURL != "https://signed.example/cache.tar" {
		t.Fatalf("expected signed URL, got %q", resp.SignedDownloadURL)
	}
}

func TestFinalizeCacheEntryUpload_SizeMismatchConflict(t *testing.T) {
	h := setupEndpointHandler(t)
	h.finalizeWaitTimeout = 20 * time.Millisecond
	h.finalizePoll = 5 * time.Millisecond

	router := h.Router()
	upload := createTestUpload(t, h, "key-size-mismatch", "v1", "folder-size-mismatch")
	createTestPartNumeric(t, h, upload.FolderName, 0, []byte("1234"))

	req := httptest.NewRequest(http.MethodPost, "/twirp/github.actions.results.api.v1.CacheService/FinalizeCacheEntryUpload", strings.NewReader(`{"key":"key-size-mismatch","version":"v1","size_bytes":10}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 for size mismatch, got %d (%s)", rr.Code, rr.Body.String())
	}

	uploadAfter, err := h.db.GetUpload(context.Background(), upload.ID)
	if err != nil {
		t.Fatalf("failed to read upload: %v", err)
	}
	if uploadAfter == nil {
		t.Fatalf("expected upload to remain for failed finalize")
	}
}

func TestFinalizeCacheEntryUpload_SuccessAndNoParts(t *testing.T) {
	h := setupEndpointHandler(t)
	router := h.Router()

	successUpload := createTestUpload(t, h, "key-finalize-success", "v1", "folder-finalize-success")
	createTestPartNumeric(t, h, successUpload.FolderName, 0, []byte("hello"))

	successReq := httptest.NewRequest(http.MethodPost, "/twirp/github.actions.results.api.v1.CacheService/FinalizeCacheEntryUpload", strings.NewReader(`{"key":"key-finalize-success","version":"v1","size_bytes":5}`))
	successReq.Header.Set("Content-Type", "application/json")
	successRR := httptest.NewRecorder()
	router.ServeHTTP(successRR, successReq)

	if successRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", successRR.Code, successRR.Body.String())
	}
	successResp := decodeJSONBody[FinalizeCacheEntryUploadResponse](t, successRR)
	if !successResp.OK {
		t.Fatalf("expected finalize ok=true")
	}
	if successResp.EntryID != strconv.FormatInt(successUpload.ID, 10) {
		t.Fatalf("expected entry_id %d, got %q", successUpload.ID, successResp.EntryID)
	}

	noPartsUpload := createTestUpload(t, h, "key-no-parts", "v1", "folder-no-parts")
	_ = noPartsUpload
	noPartsReq := httptest.NewRequest(http.MethodPost, "/twirp/github.actions.results.api.v1.CacheService/FinalizeCacheEntryUpload", strings.NewReader(`{"key":"key-no-parts","version":"v1"}`))
	noPartsReq.Header.Set("Content-Type", "application/json")
	noPartsRR := httptest.NewRecorder()
	router.ServeHTTP(noPartsRR, noPartsReq)

	if noPartsRR.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for no parts, got %d (%s)", noPartsRR.Code, noPartsRR.Body.String())
	}
}

func TestUploadEndpoint_Behaviors(t *testing.T) {
	h := setupEndpointHandler(t)
	router := h.Router()

	upload := createTestUpload(t, h, "key-upload", "v1", "folder-upload")
	blockID := makeBlockID64(7)

	okReq := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/upload/%d?blockid=%s", upload.ID, blockID), strings.NewReader("chunk-seven"))
	okRR := httptest.NewRecorder()
	router.ServeHTTP(okRR, okReq)
	if okRR.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%s)", okRR.Code, okRR.Body.String())
	}

	reader, err := h.storage.CreateDownloadStream(context.Background(), fmt.Sprintf("%s/parts/%d", upload.FolderName, 7))
	if err != nil {
		t.Fatalf("failed to read uploaded chunk: %v", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read chunk bytes: %v", err)
	}
	if string(data) != "chunk-seven" {
		t.Fatalf("unexpected chunk data: %q", string(data))
	}

	updatedUpload, err := h.db.GetUpload(context.Background(), upload.ID)
	if err != nil {
		t.Fatalf("failed to get upload: %v", err)
	}
	if updatedUpload == nil || !updatedUpload.LastPartUploadedAt.Valid {
		t.Fatalf("expected LastPartUploadedAt to be updated")
	}

	dupReq := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/upload/%d?blockid=%s", upload.ID, blockID), strings.NewReader("new-data"))
	dupRR := httptest.NewRecorder()
	router.ServeHTTP(dupRR, dupReq)
	if dupRR.Code != http.StatusConflict {
		t.Fatalf("expected 409 on duplicate chunk, got %d (%s)", dupRR.Code, dupRR.Body.String())
	}

	invalidBlockReq := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/upload/%d?blockid=invalid-base64", upload.ID), strings.NewReader("data"))
	invalidBlockRR := httptest.NewRecorder()
	router.ServeHTTP(invalidBlockRR, invalidBlockReq)
	if invalidBlockRR.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid block id, got %d", invalidBlockRR.Code)
	}

	unknownReq := httptest.NewRequest(http.MethodPut, "/upload/9999999999?blockid="+blockID, strings.NewReader("data"))
	unknownRR := httptest.NewRecorder()
	router.ServeHTTP(unknownRR, unknownReq)
	if unknownRR.Code != http.StatusCreated {
		t.Fatalf("expected 201 for unknown upload no-op, got %d", unknownRR.Code)
	}

	blocklistReq := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/upload/%d?comp=blocklist", upload.ID), nil)
	blocklistRR := httptest.NewRecorder()
	router.ServeHTTP(blocklistRR, blocklistReq)
	if blocklistRR.Code != http.StatusCreated {
		t.Fatalf("expected 201 for blocklist finalize, got %d", blocklistRR.Code)
	}

	badUploadReq := httptest.NewRequest(http.MethodPut, "/upload/not-a-number", strings.NewReader("data"))
	badUploadRR := httptest.NewRecorder()
	router.ServeHTTP(badUploadRR, badUploadReq)
	if badUploadRR.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid upload ID, got %d", badUploadRR.Code)
	}
}

func TestUploadEndpoint_DuplicateChunkConcurrent(t *testing.T) {
	h := setupEndpointHandler(t)
	router := h.Router()

	upload := createTestUpload(t, h, "key-upload-concurrent", "v1", "folder-upload-concurrent")
	blockID := makeBlockID64(3)

	start := make(chan struct{})
	statuses := make(chan int, 2)
	payloads := []string{"first-write", "second-write"}

	var wg sync.WaitGroup
	for _, payload := range payloads {
		wg.Add(1)
		go func(data string) {
			defer wg.Done()
			<-start
			req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/upload/%d?blockid=%s", upload.ID, blockID), strings.NewReader(data))
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			statuses <- rr.Code
		}(payload)
	}

	close(start)
	wg.Wait()
	close(statuses)

	var successCount, conflictCount int
	for status := range statuses {
		switch status {
		case http.StatusCreated:
			successCount++
		case http.StatusConflict:
			conflictCount++
		default:
			t.Fatalf("unexpected status for concurrent upload: %d", status)
		}
	}

	if successCount != 1 || conflictCount != 1 {
		t.Fatalf("expected one success and one conflict, got success=%d conflict=%d", successCount, conflictCount)
	}

	reader, err := h.storage.CreateDownloadStream(context.Background(), fmt.Sprintf("%s/parts/%d", upload.FolderName, 3))
	if err != nil {
		t.Fatalf("failed to open stored chunk: %v", err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read stored chunk: %v", err)
	}

	if string(body) != "first-write" && string(body) != "second-write" {
		t.Fatalf("unexpected stored body after concurrent upload: %q", string(body))
	}
}

func TestDownloadEndpoint_Behaviors(t *testing.T) {
	h := setupEndpointHandler(t)
	router := h.Router()

	notFoundReq := httptest.NewRequest(http.MethodGet, "/download/missing", nil)
	notFoundRR := httptest.NewRecorder()
	router.ServeHTTP(notFoundRR, notFoundReq)
	if notFoundRR.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing cache entry, got %d", notFoundRR.Code)
	}

	missingLocEntryID := uuid.New().String()
	err := h.db.CreateCacheEntry(context.Background(), &db.CacheEntry{
		ID:         missingLocEntryID,
		Key:        "k-missing-loc",
		Version:    "v1",
		UpdatedAt:  time.Now().UnixMilli(),
		LocationID: "missing-loc",
	})
	if err != nil {
		t.Fatalf("failed to create missing-loc cache entry: %v", err)
	}

	missingLocReq := httptest.NewRequest(http.MethodGet, "/download/"+missingLocEntryID, nil)
	missingLocRR := httptest.NewRecorder()
	router.ServeHTTP(missingLocRR, missingLocReq)
	if missingLocRR.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing storage location, got %d", missingLocRR.Code)
	}

	streamEntryID, streamLocationID := createCacheEntryWithLocation(t, h, "k-stream", "v1", "folder-stream", 3)
	if err := h.db.UpdateStorageLocationMergeStarted(context.Background(), streamLocationID, time.Now().UnixMilli()); err != nil {
		t.Fatalf("failed to mark merge started: %v", err)
	}
	createTestPartNumeric(t, h, "folder-stream", 10, []byte("ten"))
	createTestPartNumeric(t, h, "folder-stream", 2, []byte("two"))
	createTestPartNumeric(t, h, "folder-stream", 1, []byte("one"))

	streamReq := httptest.NewRequest(http.MethodGet, "/download/"+streamEntryID, nil)
	streamRR := httptest.NewRecorder()
	router.ServeHTTP(streamRR, streamReq)
	if streamRR.Code != http.StatusOK {
		t.Fatalf("expected 200 for streamed download, got %d (%s)", streamRR.Code, streamRR.Body.String())
	}
	if streamRR.Body.String() != "onetwoten" {
		t.Fatalf("expected numeric order stream, got %q", streamRR.Body.String())
	}

	mergedEntryID, mergedLocationID := createCacheEntryWithLocation(t, h, "k-merged", "v1", "folder-merged", 1)
	if err := h.storage.UploadStream(context.Background(), "folder-merged/merged", bytes.NewReader([]byte("merged-content"))); err != nil {
		t.Fatalf("failed to upload merged file: %v", err)
	}
	if err := h.db.UpdateStorageLocationMerged(context.Background(), mergedLocationID, time.Now().UnixMilli()); err != nil {
		t.Fatalf("failed to mark merged: %v", err)
	}

	mergedReq := httptest.NewRequest(http.MethodGet, "/download/"+mergedEntryID, nil)
	mergedRR := httptest.NewRecorder()
	router.ServeHTTP(mergedRR, mergedReq)
	if mergedRR.Code != http.StatusOK {
		t.Fatalf("expected 200 for merged download, got %d (%s)", mergedRR.Code, mergedRR.Body.String())
	}
	if mergedRR.Body.String() != "merged-content" {
		t.Fatalf("unexpected merged content: %q", mergedRR.Body.String())
	}
}

func TestProxyEndpoint_SuccessAndError(t *testing.T) {
	h := setupEndpointHandler(t)
	h.proxyTargetURL = "https://proxy.test"

	h.proxyTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/proxy/success" {
			t.Fatalf("unexpected upstream path: %q", req.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Body:       io.NopCloser(strings.NewReader("proxied")),
			Header:     make(http.Header),
		}, nil
	})
	router := h.Router()

	successReq := httptest.NewRequest(http.MethodGet, "/proxy/success?x=1", nil)
	successRR := httptest.NewRecorder()
	router.ServeHTTP(successRR, successReq)
	if successRR.Code != http.StatusAccepted {
		t.Fatalf("expected 202 from proxy, got %d (%s)", successRR.Code, successRR.Body.String())
	}
	if successRR.Body.String() != "proxied" {
		t.Fatalf("unexpected proxy body: %q", successRR.Body.String())
	}

	h.proxyTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("upstream unavailable")
	})
	errorReq := httptest.NewRequest(http.MethodGet, "/proxy/failure", nil)
	errorRR := httptest.NewRecorder()
	router.ServeHTTP(errorRR, errorReq)
	if errorRR.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 from proxy error handler, got %d (%s)", errorRR.Code, errorRR.Body.String())
	}
}

func TestFaultInjection_StorageAndFinalizeErrors(t *testing.T) {
	h := setupEndpointHandler(t)
	router := h.Router()

	upload := createTestUpload(t, h, "key-fault-upload", "v1", "folder-fault-upload")
	h.storage = &hookStorageAdapter{
		base:      h.storage,
		uploadErr: errors.New("upload failed"),
	}

	uploadReq := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/upload/%d?blockid=%s", upload.ID, makeBlockID64(1)), strings.NewReader("data"))
	uploadRR := httptest.NewRecorder()
	router.ServeHTTP(uploadRR, uploadReq)
	if uploadRR.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for upload storage error, got %d (%s)", uploadRR.Code, uploadRR.Body.String())
	}

	h2 := setupEndpointHandler(t)
	h2.storage = &hookStorageAdapter{
		base:     h2.storage,
		countErr: errors.New("count failed"),
	}
	router2 := h2.Router()
	fUpload := createTestUpload(t, h2, "key-fault-finalize", "v1", "folder-fault-finalize")
	createTestPartNumeric(t, h2, fUpload.FolderName, 0, []byte("chunk"))

	finalizeReq := httptest.NewRequest(http.MethodPost, "/twirp/github.actions.results.api.v1.CacheService/FinalizeCacheEntryUpload", strings.NewReader(`{"key":"key-fault-finalize","version":"v1"}`))
	finalizeReq.Header.Set("Content-Type", "application/json")
	finalizeRR := httptest.NewRecorder()
	router2.ServeHTTP(finalizeRR, finalizeReq)
	if finalizeRR.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for finalize storage error, got %d (%s)", finalizeRR.Code, finalizeRR.Body.String())
	}
}

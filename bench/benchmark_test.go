package bench

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/falcosecurity/github-actions-cache-server/internal/config"
	"github.com/falcosecurity/github-actions-cache-server/internal/server"
)

var (
	defaultFileSizesMB   = []int{100, 500}
	defaultBufferSizesKB = []int{64, 128, 256, 512, 1024}
)

func BenchmarkCacheServer(b *testing.B) {
	fileSizes := parseEnvIntList(b, "BENCH_FILE_SIZES_MB", defaultFileSizesMB)
	bufferSizes := parseEnvIntList(b, "BENCH_BUFFER_SIZES_KB", defaultBufferSizesKB)

	for _, bufferKB := range bufferSizes {
		bufferBytes := bufferKB * 1024

		b.Run(fmt.Sprintf("buffer_%dKB", bufferKB), func(b *testing.B) {
			baseURL, stop := startServer(b, bufferBytes)
			defer stop()

			client := newCacheClient(baseURL)

			for _, sizeMB := range fileSizes {
				sizeBytes := sizeMB * 1024 * 1024
				data := make([]byte, sizeBytes)
				if _, err := rand.Read(data); err != nil {
					b.Fatalf("failed to generate data: %v", err)
				}

				b.Run(fmt.Sprintf("file_%dMB", sizeMB), func(b *testing.B) {
					b.ReportAllocs()
					b.SetBytes(int64(sizeBytes))

					for i := 0; i < b.N; i++ {
						key := fmt.Sprintf("bench-%d-%d-%d", bufferKB, sizeMB, i)
						version := "v1"

						createResp, err := client.createCacheEntry(key, version)
						if err != nil {
							b.Fatalf("create cache entry failed: %v", err)
						}
						if !createResp.OK {
							b.Fatalf("create cache entry returned ok=false")
						}

						if err := client.upload(createResp.SignedUploadURL, data); err != nil {
							b.Fatalf("upload failed: %v", err)
						}

						if _, err := client.finalizeCacheEntry(key, version); err != nil {
							b.Fatalf("finalize cache entry failed: %v", err)
						}

						downloadResp, err := client.getDownloadURL(key, version)
						if err != nil {
							b.Fatalf("get download url failed: %v", err)
						}
						if !downloadResp.OK {
							b.Fatalf("get download url returned ok=false")
						}

						downloaded, err := client.download(downloadResp.SignedDownloadURL)
						if err != nil {
							b.Fatalf("download failed: %v", err)
						}
						if len(downloaded) != len(data) {
							b.Fatalf("download size mismatch: got %d, want %d", len(downloaded), len(data))
						}
					}
				})
			}
		})
	}
}

type cacheClient struct {
	baseURL string
	client  *http.Client
}

func newCacheClient(baseURL string) *cacheClient {
	return &cacheClient{
		baseURL: baseURL,
		client: &http.Client{
			Transport: &http.Transport{
				DisableCompression:  true,
				MaxIdleConnsPerHost: 100,
			},
			Timeout: 0,
		},
	}
}

type createCacheEntryRequest struct {
	Key     string `json:"key"`
	Version string `json:"version"`
}

type createCacheEntryResponse struct {
	OK              bool   `json:"ok"`
	SignedUploadURL string `json:"signed_upload_url"`
}

func (c *cacheClient) createCacheEntry(key, version string) (*createCacheEntryResponse, error) {
	reqBody := createCacheEntryRequest{Key: key, Version: version}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := c.baseURL + "/twirp/github.actions.results.api.v1.CacheService/CreateCacheEntry"
	resp, err := c.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("post create cache entry: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create cache entry status %d: %s", resp.StatusCode, string(body))
	}

	var result createCacheEntryResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode create cache entry: %w", err)
	}

	return &result, nil
}

func (c *cacheClient) upload(uploadURL string, data []byte) error {
	req, err := http.NewRequest(http.MethodPut, uploadURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create upload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("upload request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

type finalizeCacheEntryRequest struct {
	Key     string `json:"key"`
	Version string `json:"version"`
}

type finalizeCacheEntryResponse struct {
	OK      bool   `json:"ok"`
	EntryID string `json:"entry_id"`
}

func (c *cacheClient) finalizeCacheEntry(key, version string) (*finalizeCacheEntryResponse, error) {
	reqBody := finalizeCacheEntryRequest{Key: key, Version: version}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal finalize request: %w", err)
	}

	url := c.baseURL + "/twirp/github.actions.results.api.v1.CacheService/FinalizeCacheEntryUpload"
	resp, err := c.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("post finalize cache entry: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("finalize cache entry status %d: %s", resp.StatusCode, string(body))
	}

	var result finalizeCacheEntryResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode finalize cache entry: %w", err)
	}

	return &result, nil
}

type getDownloadURLRequest struct {
	Key     string `json:"key"`
	Version string `json:"version"`
}

type getDownloadURLResponse struct {
	OK                bool   `json:"ok"`
	SignedDownloadURL string `json:"signed_download_url"`
	MatchedKey        string `json:"matched_key"`
}

func (c *cacheClient) getDownloadURL(key, version string) (*getDownloadURLResponse, error) {
	reqBody := getDownloadURLRequest{Key: key, Version: version}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal get download url request: %w", err)
	}

	url := c.baseURL + "/twirp/github.actions.results.api.v1.CacheService/GetCacheEntryDownloadURL"
	resp, err := c.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("post get download url: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get download url status %d: %s", resp.StatusCode, string(body))
	}

	var result getDownloadURLResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode get download url: %w", err)
	}

	return &result, nil
}

func (c *cacheClient) download(downloadURL string) ([]byte, error) {
	resp, err := c.client.Get(downloadURL)
	if err != nil {
		return nil, fmt.Errorf("download request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("download status %d: %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read download body: %w", err)
	}

	return data, nil
}

func startServer(b testing.TB, bufferBytes int) (string, func()) {
	b.Helper()

	tempDir := b.TempDir()
	storagePath := filepath.Join(tempDir, "storage")
	dbPath := filepath.Join(tempDir, "benchmark.db")

	if err := os.MkdirAll(storagePath, 0755); err != nil {
		b.Fatalf("failed to create storage directory: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("failed to allocate port: %v", err)
	}

	baseURL := fmt.Sprintf("http://%s", listener.Addr().String())
	port := listener.Addr().(*net.TCPAddr).Port

	cfg := &config.Config{
		StorageDriver:         "filesystem",
		StorageHighWaterMark:  bufferBytes,
		StorageFilesystemPath: storagePath,
		DBDriver:              "sqlite",
		DBSqlitePath:          dbPath,
		APIBaseURL:            baseURL,
		Port:                  port,
		MetricsEnabled:        false,
		Debug:                 false,
		Benchmark:             true,
		DisableCleanupJobs:    true,
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	app, err := server.New(context.Background(), cfg, logger)
	if err != nil {
		_ = listener.Close()
		b.Fatalf("failed to start server: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Serve(listener)
	}()

	select {
	case err := <-errCh:
		b.Fatalf("server failed to start: %v", err)
	default:
	}

	if err := waitForHealth(baseURL, 30*time.Second, errCh); err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = app.Shutdown(ctx)
		_ = listener.Close()
		b.Fatalf("server health check failed: %v", err)
	}

	stop := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = app.Shutdown(ctx)
		_ = listener.Close()
	}

	return baseURL, stop
}

func waitForHealth(baseURL string, timeout time.Duration, errCh <-chan error) error {
	deadline := time.Now().Add(timeout)
	healthURL := baseURL + "/health"

	client := &http.Client{Timeout: 5 * time.Second}

	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			return fmt.Errorf("server error: %w", err)
		default:
		}

		resp, err := client.Get(healthURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		} else if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("timeout waiting for health")
}

func parseEnvIntList(b testing.TB, key string, defaultValues []int) []int {
	b.Helper()

	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultValues
	}

	parts := strings.Split(raw, ",")
	values := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		value, err := strconv.Atoi(part)
		if err != nil {
			b.Fatalf("invalid %s value %q: %v", key, part, err)
		}
		values = append(values, value)
	}

	if len(values) == 0 {
		return defaultValues
	}

	return values
}

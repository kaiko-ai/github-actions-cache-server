package bench

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	defaultFileSizesMB   = []int{100, 500}
	defaultBufferSizesKB = []int{64, 128, 256, 512, 1024}
)

var errServerDirMissing = errors.New("cmd/server directory not found")

func BenchmarkCacheServer(b *testing.B) {
	fileSizes := parseEnvIntList(b, "BENCH_FILE_SIZES_MB", defaultFileSizesMB)
	bufferSizes := parseEnvIntList(b, "BENCH_BUFFER_SIZES_KB", defaultBufferSizesKB)

	binary := buildServerBinary(b)

	for _, bufferKB := range bufferSizes {
		bufferBytes := bufferKB * 1024

		b.Run(fmt.Sprintf("buffer_%dKB", bufferKB), func(b *testing.B) {
			baseURL, stop := startServer(b, binary, bufferBytes)
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

var (
	serverBuildOnce sync.Once
	serverBuildErr  error
	serverBinary    string
)

func buildServerBinary(b testing.TB) string {
	b.Helper()

	serverBuildOnce.Do(func() {
		root := repoRoot()
		if !hasServerDir(root) {
			serverBuildErr = fmt.Errorf("%w under %s", errServerDirMissing, root)
			return
		}

		tempDir, err := os.MkdirTemp("", "cache-server-bench-*")
		if err != nil {
			serverBuildErr = err
			return
		}

		serverBinary = filepath.Join(tempDir, "cache-server")

		cmd := exec.Command("go", "build", "-o", serverBinary, "./cmd/server")
		cmd.Dir = root
		cmd.Stdout = io.Discard
		cmd.Stderr = os.Stderr

		serverBuildErr = cmd.Run()
	})

	if serverBuildErr != nil {
		if errors.Is(serverBuildErr, errServerDirMissing) {
			b.Skipf("skipping benchmarks: %v", serverBuildErr)
		}
		b.Fatalf("failed to build server: %v", serverBuildErr)
	}

	return serverBinary
}

func repoRoot() string {
	candidates := []string{}

	if workspace := strings.TrimSpace(os.Getenv("GITHUB_WORKSPACE")); workspace != "" {
		candidates = append(candidates, workspace)
	}

	if gomod := strings.TrimSpace(os.Getenv("GOMOD")); gomod != "" && gomod != os.DevNull {
		candidates = append(candidates, filepath.Dir(gomod))
	}

	if root, err := moduleRootFromGoEnv(); err == nil && root != "" {
		candidates = append(candidates, root)
	}

	if root, err := moduleRootFromGoList(); err == nil && root != "" {
		candidates = append(candidates, root)
	}

	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	candidates = append(candidates, wd)
	if filepath.Base(wd) == "bench" {
		candidates = append(candidates, filepath.Dir(wd))
	}

	for _, candidate := range candidates {
		if hasServerDir(candidate) {
			return candidate
		}
	}

	return wd
}

func moduleRootFromGoEnv() (string, error) {
	cmd := exec.Command("go", "env", "GOMOD")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD: %w", err)
	}

	gomod := strings.TrimSpace(string(output))
	if gomod == "" || gomod == os.DevNull {
		return "", fmt.Errorf("GOMOD is empty")
	}

	return filepath.Dir(gomod), nil
}

func moduleRootFromGoList() (string, error) {
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("go list -m: %w", err)
	}

	root := strings.TrimSpace(string(output))
	if root == "" {
		return "", fmt.Errorf("module root is empty")
	}
	return root, nil
}

func hasServerDir(root string) bool {
	if root == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(root, "cmd", "server"))
	if err != nil {
		return false
	}
	return info.IsDir()
}

func startServer(b testing.TB, binary string, bufferBytes int) (string, func()) {
	b.Helper()

	tempDir := b.TempDir()
	storagePath := filepath.Join(tempDir, "storage")
	dbPath := filepath.Join(tempDir, "benchmark.db")

	if err := os.MkdirAll(storagePath, 0755); err != nil {
		b.Fatalf("failed to create storage directory: %v", err)
	}

	port := freePort(b)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(),
		"PORT="+strconv.Itoa(port),
		"API_BASE_URL="+baseURL,
		"STORAGE_DRIVER=filesystem",
		"STORAGE_FILESYSTEM_PATH="+storagePath,
		"DB_DRIVER=sqlite",
		"DB_SQLITE_PATH="+dbPath,
		fmt.Sprintf("STORAGE_HIGH_WATER_MARK=%d", bufferBytes),
		"METRICS_ENABLED=false",
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		b.Fatalf("failed to start server: %v", err)
	}

	if err := waitForHealth(baseURL, 30*time.Second); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		b.Fatalf("server health check failed: %v", err)
	}

	stop := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}

	return baseURL, stop
}

func waitForHealth(baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	healthURL := baseURL + "/health"

	client := &http.Client{Timeout: 5 * time.Second}

	for time.Now().Before(deadline) {
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

func freePort(b testing.TB) int {
	b.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("failed to allocate port: %v", err)
	}
	defer l.Close()

	return l.Addr().(*net.TCPAddr).Port
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

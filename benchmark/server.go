package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ServerManager handles server lifecycle for benchmarks
type ServerManager struct {
	projectRoot string
	tempDir     string
	cmd         *exec.Cmd
	baseURL     string
}

// NewServerManager creates a new server manager
func NewServerManager(projectRoot, tempDir string) *ServerManager {
	return &ServerManager{
		projectRoot: projectRoot,
		tempDir:     tempDir,
		baseURL:     "http://localhost:3000",
	}
}

// Build compiles the server
func (s *ServerManager) Build() error {
	cmd := exec.Command("pnpm", "run", "build")
	cmd.Dir = s.projectRoot
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to build server: %w", err)
	}
	return nil
}

// Start launches the server with the specified buffer size
func (s *ServerManager) Start(bufferSize int) error {
	storagePath := filepath.Join(s.tempDir, "storage")
	dbPath := filepath.Join(s.tempDir, "benchmark.db")

	// Clean up storage from previous run
	os.RemoveAll(storagePath)
	os.Remove(dbPath)

	if err := os.MkdirAll(storagePath, 0755); err != nil {
		return fmt.Errorf("failed to create storage directory: %w", err)
	}

	s.cmd = exec.Command("pnpm", "node", "--expose-gc", ".output/server/index.mjs")
	s.cmd.Dir = s.projectRoot
	s.cmd.Env = append(os.Environ(),
		"API_BASE_URL="+s.baseURL,
		"STORAGE_DRIVER=filesystem",
		"STORAGE_FILESYSTEM_PATH="+storagePath,
		"DB_DRIVER=sqlite",
		"DB_SQLITE_PATH="+dbPath,
		fmt.Sprintf("STORAGE_HIGH_WATER_MARK=%d", bufferSize),
		"METRICS_ENABLED=false",
	)
	s.cmd.Stdout = os.Stderr
	s.cmd.Stderr = os.Stderr

	if err := s.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start server: %w", err)
	}

	// Wait for server to be healthy
	if err := s.waitForHealth(30 * time.Second); err != nil {
		s.Stop()
		return err
	}

	return nil
}

// Stop terminates the server
func (s *ServerManager) Stop() error {
	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}

	if err := s.cmd.Process.Kill(); err != nil {
		return fmt.Errorf("failed to kill server: %w", err)
	}

	s.cmd.Wait()
	s.cmd = nil

	// Clean up storage
	storagePath := filepath.Join(s.tempDir, "storage")
	dbPath := filepath.Join(s.tempDir, "benchmark.db")
	os.RemoveAll(storagePath)
	os.Remove(dbPath)

	return nil
}

// BaseURL returns the server base URL
func (s *ServerManager) BaseURL() string {
	return s.baseURL
}

// waitForHealth polls the health endpoint until the server is ready
func (s *ServerManager) waitForHealth(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	client := &http.Client{Timeout: 5 * time.Second}
	healthURL := s.baseURL + "/health"

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("server health check timed out after %v", timeout)
		case <-ticker.C:
			resp, err := client.Get(healthURL)
			if err == nil && resp.StatusCode == http.StatusOK {
				resp.Body.Close()
				return nil
			}
			if resp != nil {
				resp.Body.Close()
			}
		}
	}
}

// CleanupStorage removes storage between benchmark runs
func (s *ServerManager) CleanupStorage() error {
	storagePath := filepath.Join(s.tempDir, "storage")
	if err := os.RemoveAll(storagePath); err != nil {
		return fmt.Errorf("failed to remove storage: %w", err)
	}
	if err := os.MkdirAll(storagePath, 0755); err != nil {
		return fmt.Errorf("failed to recreate storage: %w", err)
	}
	return nil
}

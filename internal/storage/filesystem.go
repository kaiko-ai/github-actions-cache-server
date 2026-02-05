package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// FilesystemAdapter implements storage on the local filesystem.
type FilesystemAdapter struct {
	rootPath      string
	highWaterMark int
}

// NewFilesystemAdapter creates a new filesystem storage adapter.
func NewFilesystemAdapter(rootPath string, highWaterMark int) (*FilesystemAdapter, error) {
	// Ensure the root path exists
	if err := os.MkdirAll(rootPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create storage directory: %w", err)
	}

	return &FilesystemAdapter{
		rootPath:      rootPath,
		highWaterMark: highWaterMark,
	}, nil
}

// CreateDownloadStream opens a file for reading.
func (f *FilesystemAdapter) CreateDownloadStream(ctx context.Context, objectName string) (io.ReadCloser, error) {
	path := filepath.Join(f.rootPath, objectName)
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &ObjectNotFoundError{ObjectName: objectName}
		}
		return nil, err
	}
	return file, nil
}

// UploadStream writes data to a file.
func (f *FilesystemAdapter) UploadStream(ctx context.Context, objectName string, r io.Reader) error {
	path := filepath.Join(f.rootPath, objectName)

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create parent directory: %w", err)
	}

	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	// Use a buffered copy with the configured high water mark
	buf := make([]byte, f.highWaterMark)
	_, err = io.CopyBuffer(file, r, buf)
	return err
}

// DeleteFolder removes a folder and all its contents.
func (f *FilesystemAdapter) DeleteFolder(ctx context.Context, folderName string) error {
	path := filepath.Join(f.rootPath, folderName)
	return os.RemoveAll(path)
}

// CountFilesInFolder counts files in a folder (non-recursive).
func (f *FilesystemAdapter) CountFilesInFolder(ctx context.Context, folderName string) (int, error) {
	path := filepath.Join(f.rootPath, folderName)
	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			count++
		}
	}
	return count, nil
}

// ListFilesInFolder lists all files in a folder (non-recursive).
func (f *FilesystemAdapter) ListFilesInFolder(ctx context.Context, folderName string) ([]string, error) {
	path := filepath.Join(f.rootPath, folderName)
	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			files = append(files, entry.Name())
		}
	}
	return files, nil
}

// GetFolderSize returns the total size of all files in a folder.
func (f *FilesystemAdapter) GetFolderSize(ctx context.Context, folderName string) (int64, error) {
	path := filepath.Join(f.rootPath, folderName)
	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	var total int64
	for _, entry := range entries {
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				continue
			}
			total += info.Size()
		}
	}
	return total, nil
}

// CreateDownloadURL returns empty string as filesystem doesn't support signed URLs.
func (f *FilesystemAdapter) CreateDownloadURL(ctx context.Context, objectName string, expiry time.Duration) (string, error) {
	return "", nil
}

// Close is a no-op for filesystem adapter.
func (f *FilesystemAdapter) Close() error {
	return nil
}

// HighWaterMark returns the buffer size used for streaming.
func (f *FilesystemAdapter) HighWaterMark() int {
	return f.highWaterMark
}

// Package storage provides storage adapters for the cache server.
package storage

import (
	"context"
	"io"
	"time"
)

// Adapter defines the interface for storage backends.
type Adapter interface {
	// CreateDownloadStream creates a readable stream for the given object.
	CreateDownloadStream(ctx context.Context, objectName string) (io.ReadCloser, error)

	// UploadStream uploads data from a reader to the given object.
	UploadStream(ctx context.Context, objectName string, r io.Reader) error

	// DeleteFolder deletes all objects in a folder.
	DeleteFolder(ctx context.Context, folderName string) error

	// CountFilesInFolder counts the number of files in a folder.
	CountFilesInFolder(ctx context.Context, folderName string) (int, error)

	// ListFilesInFolder lists all files in a folder (non-recursive).
	ListFilesInFolder(ctx context.Context, folderName string) ([]string, error)

	// GetFolderSize returns the total size of all files in a folder.
	GetFolderSize(ctx context.Context, folderName string) (int64, error)

	// CreateDownloadURL creates a signed URL for downloading an object (optional).
	// Returns empty string if not supported.
	CreateDownloadURL(ctx context.Context, objectName string, expiry time.Duration) (string, error)

	// Close releases any resources held by the adapter.
	Close() error
}

// ObjectNotFoundError is returned when an object is not found.
type ObjectNotFoundError struct {
	ObjectName string
}

func (e *ObjectNotFoundError) Error() string {
	return "object not found: " + e.ObjectName
}

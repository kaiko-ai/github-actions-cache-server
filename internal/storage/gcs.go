package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

const (
	gcsKeyPrefix     = "gh-actions-cache"
	gcsURLExpiration = 10 * time.Minute
)

// GCSAdapter implements storage on Google Cloud Storage.
type GCSAdapter struct {
	client        *storage.Client
	bucket        string
	highWaterMark int
}

// GCSConfig holds GCS configuration.
type GCSConfig struct {
	Bucket            string
	ServiceAccountKey string
	Endpoint          string
	HighWaterMark     int
}

// NewGCSAdapter creates a new GCS storage adapter.
func NewGCSAdapter(ctx context.Context, cfg GCSConfig) (*GCSAdapter, error) {
	var opts []option.ClientOption

	// Service account key file
	if cfg.ServiceAccountKey != "" {
		opts = append(opts, option.WithCredentialsFile(cfg.ServiceAccountKey))
	}

	// Custom endpoint (for testing with fake GCS)
	if cfg.Endpoint != "" {
		opts = append(opts, option.WithEndpoint(cfg.Endpoint))
	}

	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCS client: %w", err)
	}

	return &GCSAdapter{
		client:        client,
		bucket:        cfg.Bucket,
		highWaterMark: cfg.HighWaterMark,
	}, nil
}

func (g *GCSAdapter) fullKey(objectName string) string {
	return gcsKeyPrefix + "/" + objectName
}

// CreateDownloadStream downloads an object from GCS.
func (g *GCSAdapter) CreateDownloadStream(ctx context.Context, objectName string) (io.ReadCloser, error) {
	reader, err := g.client.Bucket(g.bucket).Object(g.fullKey(objectName)).NewReader(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return nil, &ObjectNotFoundError{ObjectName: objectName}
		}
		return nil, err
	}
	return reader, nil
}

// UploadStream uploads data to GCS.
func (g *GCSAdapter) UploadStream(ctx context.Context, objectName string, r io.Reader) error {
	writer := g.client.Bucket(g.bucket).Object(g.fullKey(objectName)).NewWriter(ctx)
	writer.ChunkSize = g.highWaterMark

	if _, err := io.Copy(writer, r); err != nil {
		writer.Close()
		return err
	}

	return writer.Close()
}

// DeleteFolder deletes all objects with a given prefix.
func (g *GCSAdapter) DeleteFolder(ctx context.Context, folderName string) error {
	prefix := g.fullKey(folderName) + "/"

	it := g.client.Bucket(g.bucket).Objects(ctx, &storage.Query{Prefix: prefix})
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return err
		}

		if err := g.client.Bucket(g.bucket).Object(attrs.Name).Delete(ctx); err != nil {
			return err
		}
	}

	return nil
}

// CountFilesInFolder counts objects with a given prefix.
func (g *GCSAdapter) CountFilesInFolder(ctx context.Context, folderName string) (int, error) {
	prefix := g.fullKey(folderName) + "/"

	count := 0
	it := g.client.Bucket(g.bucket).Objects(ctx, &storage.Query{Prefix: prefix})
	for {
		_, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return 0, err
		}
		count++
	}

	return count, nil
}

// ListFilesInFolder lists all files in a folder (non-recursive).
func (g *GCSAdapter) ListFilesInFolder(ctx context.Context, folderName string) ([]string, error) {
	prefix := g.fullKey(folderName) + "/"

	var files []string
	it := g.client.Bucket(g.bucket).Objects(ctx, &storage.Query{Prefix: prefix})
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}
		// Extract filename from full key (remove prefix)
		name := strings.TrimPrefix(attrs.Name, prefix)
		if name != "" && !strings.Contains(name, "/") {
			files = append(files, name)
		}
	}

	return files, nil
}

// CreateDownloadURL creates a signed URL for downloading.
func (g *GCSAdapter) CreateDownloadURL(ctx context.Context, objectName string, expiry time.Duration) (string, error) {
	url, err := g.client.Bucket(g.bucket).SignedURL(g.fullKey(objectName), &storage.SignedURLOptions{
		Method:  "GET",
		Expires: time.Now().Add(expiry),
	})
	if err != nil {
		return "", err
	}
	return url, nil
}

// Close releases resources.
func (g *GCSAdapter) Close() error {
	return g.client.Close()
}

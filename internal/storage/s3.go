package storage

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const (
	s3KeyPrefix     = "gh-actions-cache"
	s3URLExpiration = 10 * time.Minute
	s3PartSize      = 5 * 1024 * 1024 // 5MB
)

// S3Adapter implements storage on AWS S3.
type S3Adapter struct {
	client        *s3.Client
	presignClient *s3.PresignClient
	bucket        string
	highWaterMark int
}

// S3Config holds S3 configuration.
type S3Config struct {
	Bucket          string
	Region          string
	EndpointURL     string
	AccessKeyID     string
	SecretAccessKey string
	HighWaterMark   int
}

// NewS3Adapter creates a new S3 storage adapter.
func NewS3Adapter(ctx context.Context, cfg S3Config) (*S3Adapter, error) {
	var opts []func(*config.LoadOptions) error

	opts = append(opts, config.WithRegion(cfg.Region))

	// Custom endpoint (for S3-compatible services like MinIO)
	if cfg.EndpointURL != "" {
		opts = append(opts, config.WithEndpointResolverWithOptions(
			aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
				return aws.Endpoint{
					URL:               cfg.EndpointURL,
					HostnameImmutable: true,
				}, nil
			}),
		))
	}

	// Explicit credentials
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.EndpointURL != "" {
			o.UsePathStyle = true
		}
	})

	return &S3Adapter{
		client:        client,
		presignClient: s3.NewPresignClient(client),
		bucket:        cfg.Bucket,
		highWaterMark: cfg.HighWaterMark,
	}, nil
}

func (s *S3Adapter) fullKey(objectName string) string {
	return s3KeyPrefix + "/" + objectName
}

// CreateDownloadStream downloads an object from S3.
func (s *S3Adapter) CreateDownloadStream(ctx context.Context, objectName string) (io.ReadCloser, error) {
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.fullKey(objectName)),
	})
	if err != nil {
		// Check if it's a not found error
		if strings.Contains(err.Error(), "NoSuchKey") || strings.Contains(err.Error(), "NotFound") {
			return nil, &ObjectNotFoundError{ObjectName: objectName}
		}
		return nil, err
	}
	return result.Body, nil
}

// UploadStream uploads data to S3 using multipart upload for large files.
func (s *S3Adapter) UploadStream(ctx context.Context, objectName string, r io.Reader) error {
	uploader := manager.NewUploader(s.client, func(u *manager.Uploader) {
		u.PartSize = s3PartSize
		u.Concurrency = 1 // Single stream, no parallel uploads
	})

	_, err := uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.fullKey(objectName)),
		Body:   r,
	})
	return err
}

// DeleteFolder deletes all objects with a given prefix.
func (s *S3Adapter) DeleteFolder(ctx context.Context, folderName string) error {
	prefix := s.fullKey(folderName) + "/"

	// List all objects with the prefix
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return err
		}

		// Delete each object
		for _, obj := range page.Contents {
			_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: aws.String(s.bucket),
				Key:    obj.Key,
			})
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// CountFilesInFolder counts objects with a given prefix.
func (s *S3Adapter) CountFilesInFolder(ctx context.Context, folderName string) (int, error) {
	prefix := s.fullKey(folderName) + "/"

	count := 0
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return 0, err
		}
		count += len(page.Contents)
	}

	return count, nil
}

// ListFilesInFolder lists all files in a folder (non-recursive).
func (s *S3Adapter) ListFilesInFolder(ctx context.Context, folderName string) ([]string, error) {
	prefix := s.fullKey(folderName) + "/"

	var files []string
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			// Extract filename from full key (remove prefix)
			name := strings.TrimPrefix(*obj.Key, prefix)
			if name != "" && !strings.Contains(name, "/") {
				files = append(files, name)
			}
		}
	}

	return files, nil
}

// CreateDownloadURL creates a presigned URL for downloading.
func (s *S3Adapter) CreateDownloadURL(ctx context.Context, objectName string, expiry time.Duration) (string, error) {
	req, err := s.presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.fullKey(objectName)),
	}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", err
	}
	return req.URL, nil
}

// Close is a no-op for S3 adapter.
func (s *S3Adapter) Close() error {
	return nil
}

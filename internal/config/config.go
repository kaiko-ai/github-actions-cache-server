// Package config handles configuration for the cache server via CLI flags and environment variables.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Config holds all configuration for the cache server.
type Config struct {
	// Storage configuration
	StorageDriver        string // filesystem, s3, gcs
	StorageHighWaterMark int    // Buffer size for streaming (default: 1MB)

	// Filesystem storage
	StorageFilesystemPath string

	// S3 storage
	StorageS3Bucket    string
	AWSRegion          string
	AWSEndpointURL     string
	AWSAccessKeyID     string
	AWSSecretAccessKey string

	// GCS storage
	StorageGCSBucket            string
	StorageGCSServiceAccountKey string
	StorageGCSEndpoint          string

	// Database configuration
	DBDriver string // sqlite, postgres, mysql

	// Database pool configuration
	DBMaxOpenConns           int
	DBMaxIdleConns           int
	DBConnMaxLifetimeSeconds int

	// SQLite
	DBSqlitePath string

	// PostgreSQL
	DBPostgresURL      string
	DBPostgresHost     string
	DBPostgresPort     int
	DBPostgresUser     string
	DBPostgresPassword string
	DBPostgresDatabase string

	// MySQL
	DBMysqlHost     string
	DBMysqlPort     int
	DBMysqlUser     string
	DBMysqlPassword string
	DBMysqlDatabase string

	// Server configuration
	APIBaseURL     string
	Port           int
	MetricsEnabled bool
	UIEnabled      bool
	Debug          bool
	Benchmark      bool

	// Cache settings
	CacheCleanupOlderThanDays int
	DisableCleanupJobs        bool
	EnableDirectDownloads     bool
	MaxCacheSizeBytes         int64
}

// RegisterFlags registers all CLI flags on the given cobra command and sets up
// viper to read from both flags and environment variables. Env vars use
// SCREAMING_SNAKE_CASE (e.g. --storage-driver ↔ STORAGE_DRIVER).
func RegisterFlags(cmd *cobra.Command) {
	f := cmd.Flags()

	// Storage
	f.String("storage-driver", "filesystem", "Storage driver (filesystem, s3, gcs)")
	f.Int("storage-high-water-mark", 1048576, "Buffer size for streaming in bytes")
	f.String("storage-filesystem-path", ".data/storage/filesystem", "Filesystem storage path")

	// S3
	f.String("storage-s3-bucket", "", "S3 bucket name")
	f.String("aws-region", "us-east-1", "AWS region")
	f.String("aws-endpoint-url", "", "AWS endpoint URL (for S3-compatible stores)")
	f.String("aws-access-key-id", "", "AWS access key ID")
	f.String("aws-secret-access-key", "", "AWS secret access key")

	// GCS
	f.String("storage-gcs-bucket", "", "GCS bucket name")
	f.String("storage-gcs-service-account-key", "", "GCS service account key JSON")
	f.String("storage-gcs-endpoint", "", "GCS endpoint (for emulators)")

	// Database
	f.String("db-driver", "sqlite", "Database driver (sqlite, postgres, mysql)")
	f.Int("db-max-open-conns", 10, "Max open database connections")
	f.Int("db-max-idle-conns", 5, "Max idle database connections")
	f.Int("db-conn-max-lifetime-seconds", 3600, "Database connection max lifetime in seconds")

	// SQLite
	f.String("db-sqlite-path", ".data/sqlite.db", "SQLite database path")

	// PostgreSQL
	f.String("db-postgres-url", "", "PostgreSQL connection URL")
	f.String("db-postgres-host", "", "PostgreSQL host")
	f.Int("db-postgres-port", 5432, "PostgreSQL port")
	f.String("db-postgres-user", "", "PostgreSQL user")
	f.String("db-postgres-password", "", "PostgreSQL password")
	f.String("db-postgres-database", "", "PostgreSQL database name")

	// MySQL
	f.String("db-mysql-host", "", "MySQL host")
	f.Int("db-mysql-port", 3306, "MySQL port")
	f.String("db-mysql-user", "", "MySQL user")
	f.String("db-mysql-password", "", "MySQL password")
	f.String("db-mysql-database", "", "MySQL database name")

	// Server
	f.String("api-base-url", "http://localhost:3000", "Public base URL for the cache server")
	f.Int("port", 3000, "HTTP listen port")
	f.Bool("metrics-enabled", false, "Enable Prometheus metrics endpoint")
	f.Bool("ui-enabled", true, "Enable web UI for cache management")
	f.Bool("debug", false, "Enable debug logging")
	f.Bool("benchmark", false, "Enable benchmark mode")

	// Cache settings
	f.Int("cache-cleanup-older-than-days", 90, "Delete cache entries older than N days")
	f.Bool("disable-cleanup-jobs", false, "Disable all scheduled cleanup jobs")
	f.Bool("enable-direct-downloads", false, "Enable signed direct download URLs")
	f.Int64("max-cache-size-bytes", 0, "Maximum total cache size in bytes (0 = unlimited)")

	// Wire up viper: flags → viper ← env vars
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()
	viper.BindPFlags(f)
}

// Load reads configuration from viper (which merges CLI flags and env vars).
// RegisterFlags must be called before Load.
func Load() (*Config, error) {
	cfg := &Config{
		StorageDriver:         viper.GetString("storage-driver"),
		StorageHighWaterMark:  viper.GetInt("storage-high-water-mark"),
		StorageFilesystemPath: viper.GetString("storage-filesystem-path"),

		StorageS3Bucket:    viper.GetString("storage-s3-bucket"),
		AWSRegion:          viper.GetString("aws-region"),
		AWSEndpointURL:     viper.GetString("aws-endpoint-url"),
		AWSAccessKeyID:     viper.GetString("aws-access-key-id"),
		AWSSecretAccessKey: viper.GetString("aws-secret-access-key"),

		StorageGCSBucket:            viper.GetString("storage-gcs-bucket"),
		StorageGCSServiceAccountKey: viper.GetString("storage-gcs-service-account-key"),
		StorageGCSEndpoint:          viper.GetString("storage-gcs-endpoint"),

		DBDriver:                 viper.GetString("db-driver"),
		DBMaxOpenConns:           viper.GetInt("db-max-open-conns"),
		DBMaxIdleConns:           viper.GetInt("db-max-idle-conns"),
		DBConnMaxLifetimeSeconds: viper.GetInt("db-conn-max-lifetime-seconds"),

		DBSqlitePath: viper.GetString("db-sqlite-path"),

		DBPostgresURL:      viper.GetString("db-postgres-url"),
		DBPostgresHost:     viper.GetString("db-postgres-host"),
		DBPostgresPort:     viper.GetInt("db-postgres-port"),
		DBPostgresUser:     viper.GetString("db-postgres-user"),
		DBPostgresPassword: viper.GetString("db-postgres-password"),
		DBPostgresDatabase: viper.GetString("db-postgres-database"),

		DBMysqlHost:     viper.GetString("db-mysql-host"),
		DBMysqlPort:     viper.GetInt("db-mysql-port"),
		DBMysqlUser:     viper.GetString("db-mysql-user"),
		DBMysqlPassword: viper.GetString("db-mysql-password"),
		DBMysqlDatabase: viper.GetString("db-mysql-database"),

		APIBaseURL:     viper.GetString("api-base-url"),
		Port:           viper.GetInt("port"),
		MetricsEnabled: viper.GetBool("metrics-enabled"),
		UIEnabled:      viper.GetBool("ui-enabled"),
		Debug:          viper.GetBool("debug"),
		Benchmark:      viper.GetBool("benchmark"),

		CacheCleanupOlderThanDays: viper.GetInt("cache-cleanup-older-than-days"),
		DisableCleanupJobs:        viper.GetBool("disable-cleanup-jobs"),
		EnableDirectDownloads:     viper.GetBool("enable-direct-downloads"),
		MaxCacheSizeBytes:         viper.GetInt64("max-cache-size-bytes"),
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Validate checks that the configuration is valid.
func (c *Config) Validate() error {
	// Validate API base URL
	if c.APIBaseURL == "" {
		c.APIBaseURL = "http://localhost:3000"
	}
	if _, err := url.Parse(c.APIBaseURL); err != nil {
		return fmt.Errorf("invalid API_BASE_URL: %w", err)
	}

	// Validate storage driver
	switch c.StorageDriver {
	case "filesystem":
		// No additional validation needed
	case "s3":
		if c.StorageS3Bucket == "" {
			return errors.New("STORAGE_S3_BUCKET is required when using S3 storage")
		}
	case "gcs":
		if c.StorageGCSBucket == "" {
			return errors.New("STORAGE_GCS_BUCKET is required when using GCS storage")
		}
	default:
		return fmt.Errorf("invalid STORAGE_DRIVER: %s (must be filesystem, s3, or gcs)", c.StorageDriver)
	}

	// Validate database driver
	switch c.DBDriver {
	case "sqlite":
		// No additional validation needed
	case "postgres":
		if c.DBPostgresURL == "" && c.DBPostgresHost == "" {
			return errors.New("either DB_POSTGRES_URL or DB_POSTGRES_HOST is required when using PostgreSQL")
		}
	case "mysql":
		if c.DBMysqlHost == "" {
			return errors.New("DB_MYSQL_HOST is required when using MySQL")
		}
	default:
		return fmt.Errorf("invalid DB_DRIVER: %s (must be sqlite, postgres, or mysql)", c.DBDriver)
	}

	if c.DBMaxOpenConns < 0 {
		return errors.New("DB_MAX_OPEN_CONNS must be >= 0")
	}
	if c.DBMaxIdleConns < 0 {
		return errors.New("DB_MAX_IDLE_CONNS must be >= 0")
	}
	if c.DBConnMaxLifetimeSeconds < 0 {
		return errors.New("DB_CONN_MAX_LIFETIME_SECONDS must be >= 0")
	}
	if c.DBMaxOpenConns > 0 && c.DBMaxIdleConns > c.DBMaxOpenConns {
		return errors.New("DB_MAX_IDLE_CONNS cannot exceed DB_MAX_OPEN_CONNS")
	}

	return nil
}

// GetPostgresConnectionString returns the PostgreSQL connection string.
func (c *Config) GetPostgresConnectionString() string {
	if c.DBPostgresURL != "" {
		return c.DBPostgresURL
	}
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		c.DBPostgresHost, c.DBPostgresPort, c.DBPostgresUser, c.DBPostgresPassword, c.DBPostgresDatabase,
	)
}

// GetMySQLConnectionString returns the MySQL connection string.
func (c *Config) GetMySQLConnectionString() string {
	return fmt.Sprintf(
		"%s:%s@tcp(%s:%d)/%s?parseTime=true",
		c.DBMysqlUser, c.DBMysqlPassword, c.DBMysqlHost, c.DBMysqlPort, c.DBMysqlDatabase,
	)
}

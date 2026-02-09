// Package config handles environment-based configuration for the cache server.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
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
	Debug          bool
	Benchmark      bool

	// Cache settings
	CacheCleanupOlderThanDays int
	DisableCleanupJobs        bool
	EnableDirectDownloads     bool
}

// Load reads configuration from environment variables.
func Load() (*Config, error) {
	cfg := &Config{
		// Storage defaults
		StorageDriver:         getEnvOrDefault("STORAGE_DRIVER", "filesystem"),
		StorageHighWaterMark:  getEnvIntOrDefault("STORAGE_HIGH_WATER_MARK", 1048576),
		StorageFilesystemPath: getEnvOrDefault("STORAGE_FILESYSTEM_PATH", ".data/storage/filesystem"),

		// S3 defaults
		StorageS3Bucket:    os.Getenv("STORAGE_S3_BUCKET"),
		AWSRegion:          getEnvOrDefault("AWS_REGION", "us-east-1"),
		AWSEndpointURL:     os.Getenv("AWS_ENDPOINT_URL"),
		AWSAccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
		AWSSecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),

		// GCS defaults
		StorageGCSBucket:            os.Getenv("STORAGE_GCS_BUCKET"),
		StorageGCSServiceAccountKey: os.Getenv("STORAGE_GCS_SERVICE_ACCOUNT_KEY"),
		StorageGCSEndpoint:          os.Getenv("STORAGE_GCS_ENDPOINT"),

		// Database defaults
		DBDriver:                 getEnvOrDefault("DB_DRIVER", "sqlite"),
		DBSqlitePath:             getEnvOrDefault("DB_SQLITE_PATH", ".data/sqlite.db"),
		DBMaxOpenConns:           getEnvIntOrDefault("DB_MAX_OPEN_CONNS", 10),
		DBMaxIdleConns:           getEnvIntOrDefault("DB_MAX_IDLE_CONNS", 5),
		DBConnMaxLifetimeSeconds: getEnvIntOrDefault("DB_CONN_MAX_LIFETIME_SECONDS", 3600),

		// PostgreSQL
		DBPostgresURL:      os.Getenv("DB_POSTGRES_URL"),
		DBPostgresHost:     os.Getenv("DB_POSTGRES_HOST"),
		DBPostgresPort:     getEnvIntOrDefault("DB_POSTGRES_PORT", 5432),
		DBPostgresUser:     os.Getenv("DB_POSTGRES_USER"),
		DBPostgresPassword: os.Getenv("DB_POSTGRES_PASSWORD"),
		DBPostgresDatabase: os.Getenv("DB_POSTGRES_DATABASE"),

		// MySQL
		DBMysqlHost:     os.Getenv("DB_MYSQL_HOST"),
		DBMysqlPort:     getEnvIntOrDefault("DB_MYSQL_PORT", 3306),
		DBMysqlUser:     os.Getenv("DB_MYSQL_USER"),
		DBMysqlPassword: os.Getenv("DB_MYSQL_PASSWORD"),
		DBMysqlDatabase: os.Getenv("DB_MYSQL_DATABASE"),

		// Server
		APIBaseURL:     os.Getenv("API_BASE_URL"),
		Port:           getEnvIntOrDefault("PORT", 3000),
		MetricsEnabled: getEnvBoolOrDefault("METRICS_ENABLED", false),
		Debug:          getEnvBoolOrDefault("DEBUG", false),
		Benchmark:      getEnvBoolOrDefault("BENCHMARK", false),

		// Cache settings
		CacheCleanupOlderThanDays: getEnvIntOrDefault("CACHE_CLEANUP_OLDER_THAN_DAYS", 90),
		DisableCleanupJobs:        getEnvBoolOrDefault("DISABLE_CLEANUP_JOBS", false),
		EnableDirectDownloads:     getEnvBoolOrDefault("ENABLE_DIRECT_DOWNLOADS", false),
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
		return errors.New("API_BASE_URL is required")
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

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvIntOrDefault(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func getEnvBoolOrDefault(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if boolValue, err := strconv.ParseBool(value); err == nil {
			return boolValue
		}
	}
	return defaultValue
}

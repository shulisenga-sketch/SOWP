package config

import "os"

type Config struct {
	Address        string
	DatabaseDriver string
	DatabasePath   string
	DatabaseDSN    string
	MigrationPath  string
	StorageRoot    string
	MaxUploadBytes int64
}

func Load() Config {
	driver := envOrDefault("SOWP_DATABASE_DRIVER", "sqlite")
	migrationPath := envOrDefault("SOWP_MIGRATION_PATH", "migrations/001_initial.sql")
	if driver == "mysql" && os.Getenv("SOWP_MIGRATION_PATH") == "" {
		migrationPath = "migrations/mysql/001_initial.sql"
	}
	return Config{
		Address:        envOrDefault("SOWP_ADDRESS", ":8080"),
		DatabaseDriver: driver,
		DatabasePath:   envOrDefault("SOWP_DATABASE_PATH", "data/sowp.db"),
		DatabaseDSN:    os.Getenv("SOWP_DATABASE_DSN"),
		MigrationPath:  migrationPath,
		StorageRoot:    envOrDefault("SOWP_STORAGE_ROOT", "storage"),
		MaxUploadBytes: 15 * 1024 * 1024,
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

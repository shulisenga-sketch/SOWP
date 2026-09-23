package database

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"
)

func Open(driver, source string) (*sql.DB, error) {
	if driver == "" {
		driver = "sqlite"
	}
	if driver == "sqlite" {
		if err := os.MkdirAll(filepath.Dir(source), 0o750); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
		source += "?_pragma=foreign_keys(1)"
	}
	if driver != "sqlite" && driver != "mysql" {
		return nil, fmt.Errorf("unsupported database driver %q", driver)
	}
	if driver == "mysql" {
		if err := registerMySQLTLS(); err != nil {
			return nil, err
		}
		source = addMySQLOption(source, "multiStatements=true")
		source = addMySQLOption(source, "parseTime=true")
	}
	db, err := sql.Open(driver, source)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return db, nil
}

func registerMySQLTLS() error {
	certificate := strings.TrimSpace(os.Getenv("SOWP_DATABASE_CA_CERT"))
	if certificate == "" {
		return fmt.Errorf("SOWP_DATABASE_CA_CERT is required for MySQL TLS")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(certificate)) {
		return fmt.Errorf("could not parse SOWP_DATABASE_CA_CERT")
	}
	if err := mysql.RegisterTLSConfig("aiven", &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12, ServerName: os.Getenv("SOWP_DATABASE_TLS_SERVER_NAME")}); err != nil {
		return fmt.Errorf("register MySQL TLS: %w", err)
	}
	return nil
}

func addMySQLOption(source, option string) string {
	key := option[:strings.IndexByte(option, '=')]
	if strings.Contains(source, key+"=") {
		return source
	}
	separator := "?"
	if strings.Contains(source, "?") {
		separator = "&"
	}
	return source + separator + option
}

func Migrate(ctx context.Context, db *sql.DB, migrationPath string) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version VARCHAR(255) PRIMARY KEY,
			applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}

	migrationPaths, err := migrationFiles(migrationPath)
	if err != nil {
		return err
	}
	for _, path := range migrationPaths {
		if err := applyMigration(ctx, db, path); err != nil {
			return err
		}
	}
	return nil
}

func migrationFiles(migrationPath string) ([]string, error) {
	directory := filepath.Dir(migrationPath)
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read migration directory: %w", err)
	}
	paths := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".sql" {
			paths = append(paths, filepath.Join(directory, entry.Name()))
		}
	}
	if len(paths) == 0 {
		return []string{migrationPath}, nil
	}
	return paths, nil
}

func applyMigration(ctx context.Context, db *sql.DB, migrationPath string) error {
	schema, err := os.ReadFile(migrationPath)
	if err != nil {
		return fmt.Errorf("read migration: %w", err)
	}
	version := filepath.Base(migrationPath)
	var applied bool
	if err := db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = ?)`, version,
	).Scan(&applied); err != nil {
		return fmt.Errorf("check migration: %w", err)
	}
	if applied {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, string(schema)); err != nil {
		return fmt.Errorf("run migration %s: %w", version, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version) VALUES (?)`, version,
	); err != nil {
		return fmt.Errorf("record migration %s: %w", version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", version, err)
	}
	return nil
}

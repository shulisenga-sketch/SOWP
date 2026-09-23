package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"sowp/internal/auth"
	"sowp/internal/config"
	"sowp/internal/database"
)

func main() {
	email := os.Getenv("SOWP_ADMIN_EMAIL")
	password := os.Getenv("SOWP_ADMIN_PASSWORD")
	if email == "" || password == "" {
		log.Fatal("SOWP_ADMIN_EMAIL and SOWP_ADMIN_PASSWORD are required")
	}
	if len(password) < 12 {
		log.Fatal("SOWP_ADMIN_PASSWORD must be at least 12 characters")
	}

	cfg := config.Load()
	databaseSource := cfg.DatabasePath
	if cfg.DatabaseDriver == "mysql" {
		databaseSource = cfg.DatabaseDSN
	}
	db, err := database.Open(cfg.DatabaseDriver, databaseSource)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(context.Background(), db, cfg.MigrationPath); err != nil {
		log.Fatal(err)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		log.Fatal(err)
	}
	var userExists bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM users WHERE email = ?)`, email).Scan(&userExists); err != nil {
		log.Fatal(err)
	}
	if userExists {
		_, err = db.Exec(`
			UPDATE users
			SET full_name = ?, password_hash = ?, role = 'admin', is_active = 1
			WHERE email = ?`, "SOWP Administrator", hash, email)
	} else {
		_, err = db.Exec(`
			INSERT INTO users (full_name, email, password_hash, role, is_active)
			VALUES (?, ?, ?, 'admin', 1)`, "SOWP Administrator", email, hash)
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("admin account ready: %s\n", email)
}

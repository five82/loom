package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationReportsInvalidDatabaseAndSchemaCreation(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(context.Background(), filepath.Join(file, "loom.db")); err == nil || !strings.Contains(err.Error(), "create database directory") {
		t.Fatalf("invalid directory = %v", err)
	}
	path := filepath.Join(root, "loom.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE libraries (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(context.Background(), path); err == nil || !strings.Contains(err.Error(), "create database schema") {
		t.Fatalf("conflicting fresh schema = %v", err)
	}
	// The failed transaction must not have advanced the version.
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 0 {
		t.Fatalf("failed creation advanced version to %d", version)
	}
}

func TestMigrationReportsBeginAndSchemaReadErrors(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := schemaVersion(db); err == nil || !strings.Contains(err.Error(), "read schema version") {
		t.Fatalf("closed version read = %v", err)
	}
	if err := applyMigrations(context.Background(), db, 1, 2, []schemaMigration{{from: 1, to: 2, sql: "SELECT 1"}}); err == nil || !strings.Contains(err.Error(), "begin schema migration") {
		t.Fatalf("closed migration = %v", err)
	}
}

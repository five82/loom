package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

// schemaMigration is an immutable upgrade from one released schema to the
// next. Once added, migrations stay here so either Loom database, or an older
// backup, can be brought forward by a current binary.
type schemaMigration struct {
	from int
	to   int
	sql  string
}

// Version 12 is the migration baseline. Fresh databases are created directly
// at currentSchemaVersion; append each future released upgrade to this list.
var schemaMigrations = []schemaMigration{{
	from: 12,
	to:   13,
	sql: `
CREATE TABLE featured_rotation (
    item_id INTEGER PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
    shown INTEGER NOT NULL DEFAULT 0 CHECK (shown IN (0, 1))
);
CREATE TABLE featured_pick (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    period_started_at TEXT NOT NULL
);
INSERT INTO featured_rotation(item_id, shown)
SELECT i.id, 0
FROM items i JOIN libraries l ON l.id = i.library_id
WHERE i.available = 1 AND i.kind = 'movie' AND l.kind = 'movies'
    AND i.vote_average >= 7.5
    AND NOT EXISTS (
        SELECT 1 FROM item_genres ig JOIN genres g ON g.id = ig.genre_id
        WHERE ig.item_id = i.id AND g.name = 'Documentary' COLLATE NOCASE
    );`,
}, {
	// Shows now store their TMDB genres, which the home screen reads for the
	// documentary shelf. Existing shows were detailed before that, so their
	// details are marked unloaded and the next scan fetches them again;
	// artwork and playback state are untouched because the refetch only
	// rewrites provider-owned text fields, genres, and credits.
	from: 13,
	to:   14,
	sql: `
UPDATE items SET details_loaded = 0 WHERE kind = 'show' AND tmdb_id <> 0;`,
}, {
	// Warp's linear channels and their rolling schedule. Both tables are new
	// and empty; the generator fills them from the existing catalog on the
	// next daemon start, so no existing row is touched.
	from: 14,
	to:   15,
	sql: `
CREATE TABLE channels (
    id INTEGER PRIMARY KEY,
    number INTEGER NOT NULL UNIQUE,
    key TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('show', 'genre', 'hdr', 'mix')),
    item_id INTEGER REFERENCES items(id) ON DELETE CASCADE,
    genre_id INTEGER REFERENCES genres(id),
    created_at TEXT NOT NULL
);
CREATE TABLE channel_programs (
    id INTEGER PRIMARY KEY,
    channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    starts_at TEXT NOT NULL,
    ends_at TEXT NOT NULL
);
CREATE INDEX channel_programs_channel_idx ON channel_programs(channel_id, starts_at);
CREATE INDEX channel_programs_ends_idx ON channel_programs(ends_at);`,
}}

// MigrationResult describes the schema change made by Migrate.
type MigrationResult struct {
	From    int
	To      int
	Created bool
}

// Migrate creates a fresh catalog at the current schema or applies every
// required released migration. Each migration and its version update commit in
// one transaction.
func Migrate(ctx context.Context, path string) (MigrationResult, error) {
	db, err := openDatabase(path)
	if err != nil {
		return MigrationResult{}, err
	}
	defer func() { _ = db.Close() }()

	version, err := schemaVersion(db)
	if err != nil {
		return MigrationResult{}, err
	}
	result := MigrationResult{From: version, To: version}
	if version == 0 {
		if err := createSchema(db); err != nil {
			return result, err
		}
		result.To = currentSchemaVersion
		result.Created = true
		return result, nil
	}
	if version == currentSchemaVersion {
		return result, nil
	}
	if err := applyMigrations(ctx, db, version, currentSchemaVersion, schemaMigrations); err != nil {
		return result, err
	}
	result.To = currentSchemaVersion
	return result, nil
}

func openDatabase(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("configure sqlite: %w", err)
		}
	}
	return db, nil
}

func schemaVersion(db *sql.DB) (int, error) {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}

func migrationPathExists(from int) bool {
	return migrationPathExistsIn(from, currentSchemaVersion, schemaMigrations)
}

func migrationPathExistsIn(from, target int, migrations []schemaMigration) bool {
	for from < target {
		matches := 0
		next := from
		for _, migration := range migrations {
			if migration.from == from {
				matches++
				next = migration.to
			}
		}
		if matches != 1 || next <= from || next > target {
			return false
		}
		from = next
	}
	return from == target
}

func applyMigrations(
	ctx context.Context,
	db *sql.DB,
	from int,
	target int,
	migrations []schemaMigration,
) error {
	if !migrationPathExistsIn(from, target, migrations) {
		return fmt.Errorf("database schema version %d cannot be migrated to version %d by this Loom binary",
			from, target)
	}
	for from < target {
		var next schemaMigration
		for _, migration := range migrations {
			if migration.from == from {
				next = migration
				break
			}
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin schema migration %d to %d: %w", next.from, next.to, err)
		}
		if _, err := tx.ExecContext(ctx, next.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migrate schema %d to %d: %w", next.from, next.to, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", next.to)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record schema version %d: %w", next.to, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit schema migration %d to %d: %w", next.from, next.to, err)
		}
		from = next.to
	}
	return nil
}

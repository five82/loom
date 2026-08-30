package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMigrateCreatesAndAcceptsCurrentSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loom.db")
	result, err := Migrate(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.From != 0 || result.To != currentSchemaVersion {
		t.Fatalf("creation result = %+v", result)
	}

	result, err = Migrate(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Created || result.From != currentSchemaVersion || result.To != currentSchemaVersion {
		t.Fatalf("no-op result = %+v", result)
	}
}

func TestApplyMigrationsPreservesRowsAndAdvancesVersion(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`
CREATE TABLE durable_state (id INTEGER PRIMARY KEY, value TEXT NOT NULL);
INSERT INTO durable_state(value) VALUES ('keep me');
PRAGMA user_version = 1;`); err != nil {
		t.Fatal(err)
	}

	migrations := []schemaMigration{
		{from: 1, to: 2, sql: `ALTER TABLE durable_state ADD COLUMN selected INTEGER NOT NULL DEFAULT 1;`},
		{from: 2, to: 3, sql: `CREATE INDEX durable_state_value_idx ON durable_state(value);`},
	}
	if err := applyMigrations(context.Background(), db, 1, 3, migrations); err != nil {
		t.Fatal(err)
	}

	var version int
	var value string
	var selected int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT value, selected FROM durable_state WHERE id = 1`).Scan(&value, &selected); err != nil {
		t.Fatal(err)
	}
	if version != 3 || value != "keep me" || selected != 1 {
		t.Fatalf("migrated state = version %d, value %q, selected %d", version, value, selected)
	}
}

func TestFailedMigrationRollsBackSchemaAndVersion(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE sample (id INTEGER PRIMARY KEY); PRAGMA user_version = 1;`); err != nil {
		t.Fatal(err)
	}

	migrations := []schemaMigration{{
		from: 1,
		to:   2,
		sql:  `ALTER TABLE sample ADD COLUMN value TEXT; INSERT INTO missing_table VALUES (1);`,
	}}
	if err := applyMigrations(context.Background(), db, 1, 2, migrations); err == nil {
		t.Fatal("migration unexpectedly succeeded")
	}

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("schema version = %d, want 1", version)
	}
	rows, err := db.Query(`PRAGMA table_info(sample)`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "value" {
			t.Fatal("failed migration left its added column behind")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationPathMustBeCompleteAndUnambiguous(t *testing.T) {
	complete := []schemaMigration{{from: 1, to: 2}, {from: 2, to: 3}}
	if !migrationPathExistsIn(1, 3, complete) {
		t.Fatal("complete migration path was rejected")
	}
	for name, migrations := range map[string][]schemaMigration{
		"gap":       {{from: 1, to: 2}},
		"duplicate": {{from: 1, to: 2}, {from: 1, to: 3}},
		"backward":  {{from: 1, to: 1}},
	} {
		t.Run(name, func(t *testing.T) {
			if migrationPathExistsIn(1, 3, migrations) {
				t.Fatal("invalid migration path was accepted")
			}
		})
	}
}

func TestMigrateRejectsSchemaWithoutAPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loom.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 5`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Migrate(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "cannot be migrated") {
		t.Fatalf("Migrate error = %v", err)
	}
}

// assertChannelTablesUsable checks the current channel tables on a migrated
// catalog: they are empty, a channel takes a number, and its programs and
// cursors follow the channel out of the catalog.
func assertChannelTablesUsable(t *testing.T, ctx context.Context, migrated *Store, ids map[string]int64) {
	t.Helper()
	channels, err := migrated.Channels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 0 {
		t.Fatalf("migrated catalog carries channels %+v", channels)
	}
	channel, err := migrated.CreateChannel(ctx, "sitcoms", "Sitcoms", ChannelTime(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if channel.Number != 1 {
		t.Fatalf("first channel number = %d, want 1", channel.Number)
	}
	if err := migrated.AppendChannelPrograms(ctx, channel.ID, []ScheduledProgram{{
		ItemID: ids["showA-e1"], StartsAt: "2026-08-29T20:00:00Z", EndsAt: "2026-08-29T20:30:00Z",
	}}, []ChannelCursor{{Source: "show-a", Cycle: 0, ItemID: ids["showA-e1"]}}); err != nil {
		t.Fatal(err)
	}
	if _, err := migrated.db.ExecContext(ctx, `DELETE FROM channels WHERE id = ?`, channel.ID); err != nil {
		t.Fatal(err)
	}
	var programRows, cursorRows int
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM channel_programs`).Scan(&programRows); err != nil {
		t.Fatal(err)
	}
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM channel_cursors`).Scan(&cursorRows); err != nil {
		t.Fatal(err)
	}
	if programRows != 0 || cursorRows != 0 {
		t.Fatalf("rows left after their channel was removed = %d programs, %d cursors", programRows, cursorRows)
	}
}

// Version 15 held the proof-of-concept lineup, ranked from the catalog, and
// its schedule. Both are dropped and rebuilt by the daemon; everything else in
// the catalog is left alone.
func TestMigrate15To16ReplacesChannelTables(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "loom.db")
	catalog, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ids := seedMixedCatalog(t, ctx, catalog)
	if _, err := catalog.SetProgress(ctx, ids["showA-e1"], 300_000, 1_200_000); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.SetPlayed(ctx, ids["movie1"]); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.UpsertImage(ctx, Image{
		ItemID: ids["showA"], Kind: "poster", Path: "/state/poster.jpg", SourceURL: "https://example/poster.jpg",
		Tag: "manual", ContentType: "image/jpeg", ManuallySelected: true, UpdatedAt: now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
DROP TABLE channel_cursors;
DROP TABLE channel_programs;
DROP TABLE channels;
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
CREATE INDEX channel_programs_ends_idx ON channel_programs(ends_at);
INSERT INTO channels(number, key, name, kind, item_id, created_at)
VALUES (1, 'show:` + fmt.Sprint(ids["showA"]) + `', 'Show A', 'show', ` + fmt.Sprint(ids["showA"]) + `, '2026-08-29T20:00:00Z');
INSERT INTO channel_programs(channel_id, item_id, starts_at, ends_at)
VALUES (1, ` + fmt.Sprint(ids["showA-e1"]) + `, '2026-08-29T20:00:00Z', '2026-08-29T20:30:00Z');
PRAGMA user_version = 15;`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := Migrate(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if result.From != 15 || result.To != currentSchemaVersion || result.Created {
		t.Fatalf("migration result = %+v", result)
	}
	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = migrated.Close() }()
	var playbackRows, manualArtwork, itemRows int
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM playback_state`).Scan(&playbackRows); err != nil {
		t.Fatal(err)
	}
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM images WHERE manually_selected = 1`).Scan(&manualArtwork); err != nil {
		t.Fatal(err)
	}
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&itemRows); err != nil {
		t.Fatal(err)
	}
	if playbackRows != 2 || manualArtwork != 1 || itemRows != 10 {
		t.Fatalf("migrated rows = playback %d, manual artwork %d, items %d",
			playbackRows, manualArtwork, itemRows)
	}
	assertChannelTablesUsable(t, ctx, migrated, ids)
}

// Version 16 already has today's channel tables; 17 only empties them so the
// rebuilt lineup is numbered in order, and everything else survives.
func TestMigrate16To17EmptiesChannelTables(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "loom.db")
	catalog, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ids := seedMixedCatalog(t, ctx, catalog)
	if _, err := catalog.SetProgress(ctx, ids["showA-e1"], 300_000, 1_200_000); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.UpsertImage(ctx, Image{
		ItemID: ids["showA"], Kind: "poster", Path: "/state/poster.jpg", SourceURL: "https://example/poster.jpg",
		Tag: "manual", ContentType: "image/jpeg", ManuallySelected: true, UpdatedAt: now(),
	}); err != nil {
		t.Fatal(err)
	}
	for number, key := range []string{"south-park", "south-park-shuffle", "classics"} {
		channel, err := catalog.CreateChannel(ctx, key, key, "2026-08-29T20:00:00Z")
		if err != nil {
			t.Fatal(err)
		}
		if channel.Number != number+1 {
			t.Fatalf("channel %q took number %d", key, channel.Number)
		}
		if err := catalog.AppendChannelPrograms(ctx, channel.ID, []ScheduledProgram{{
			ItemID: ids["showA-e1"], StartsAt: "2026-08-29T20:00:00Z", EndsAt: "2026-08-29T20:30:00Z",
		}}, []ChannelCursor{{Source: "run", ItemID: ids["showA-e1"]}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := catalog.db.ExecContext(ctx, `PRAGMA user_version = 16`); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := Migrate(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if result.From != 16 || result.To != currentSchemaVersion || result.Created {
		t.Fatalf("migration result = %+v", result)
	}
	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = migrated.Close() }()
	var playbackRows, manualArtwork, itemRows int
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM playback_state`).Scan(&playbackRows); err != nil {
		t.Fatal(err)
	}
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM images WHERE manually_selected = 1`).Scan(&manualArtwork); err != nil {
		t.Fatal(err)
	}
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&itemRows); err != nil {
		t.Fatal(err)
	}
	if playbackRows != 1 || manualArtwork != 1 || itemRows != 10 {
		t.Fatalf("migrated rows = playback %d, manual artwork %d, items %d",
			playbackRows, manualArtwork, itemRows)
	}
	// The channel tables are empty and numbering starts over at 1.
	assertChannelTablesUsable(t, ctx, migrated, ids)
}

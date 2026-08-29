package store

import (
	"context"
	"database/sql"
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

func TestMigrate12To13PreservesStateAndSeedsFeaturedRotation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "loom.db")
	catalog, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, scanID, err := catalog.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	eligible, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: libraryID, SourceKey: "Arrival", Kind: "movie", Title: "Arrival", ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	documentary, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: libraryID, SourceKey: "Documentary", Kind: "movie", Title: "Documentary", ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.UpdateMetadata(ctx, eligible, MetadataUpdate{
		TMDBID: 1, VoteAverage: 8.0, Genres: []Genre{{ID: 18, Name: "Drama"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.UpdateMetadata(ctx, documentary, MetadataUpdate{
		TMDBID: 2, VoteAverage: 9.0, Genres: []Genre{{ID: 99, Name: "Documentary"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.UpsertMedia(ctx, MediaFile{
		ItemID: eligible, Path: "/movies/Arrival.mkv", Size: 1, MTimeNS: 1,
		DurationMS: 600_000, LastSeenScanID: scanID,
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := catalog.FinishScan(ctx, libraryID, scanID, 2, 2, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.SetPlayed(ctx, eligible); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.UpsertImage(ctx, Image{
		ItemID: eligible, Kind: "poster", Path: "/state/poster.jpg", SourceURL: "https://example/poster.jpg",
		Tag: "manual", ContentType: "image/jpeg", ManuallySelected: true, UpdatedAt: now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}

	// Removing the tables added after version 12 recreates the exact version-12
	// schema shape while retaining representative catalog, playback, and
	// artwork rows.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
DROP TABLE channel_programs;
DROP TABLE channels;
DROP TABLE featured_pick;
DROP TABLE featured_rotation;
PRAGMA user_version = 12;`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := Migrate(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if result.From != 12 || result.To != currentSchemaVersion || result.Created {
		t.Fatalf("migration result = %+v", result)
	}
	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = migrated.Close() }()
	var playbackRows, manualArtwork, rotationRows int
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM playback_state`).Scan(&playbackRows); err != nil {
		t.Fatal(err)
	}
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM images WHERE manually_selected = 1`).Scan(&manualArtwork); err != nil {
		t.Fatal(err)
	}
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM featured_rotation`).Scan(&rotationRows); err != nil {
		t.Fatal(err)
	}
	if playbackRows != 1 || manualArtwork != 1 || rotationRows != 1 {
		t.Fatalf("migrated rows = playback %d, manual artwork %d, rotation %d",
			playbackRows, manualArtwork, rotationRows)
	}
	var rotatedID int64
	if err := migrated.db.QueryRow(`SELECT item_id FROM featured_rotation`).Scan(&rotatedID); err != nil {
		t.Fatal(err)
	}
	if rotatedID != eligible {
		t.Fatalf("seeded featured item = %d, want eligible movie %d", rotatedID, eligible)
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

func TestMigrate13To14MarksShowDetailsForReload(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "loom.db")
	catalog, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ids := seedMixedCatalog(t, ctx, catalog)
	if err := catalog.UpdateMetadata(ctx, ids["movie1"], MetadataUpdate{TMDBID: 1, Title: "Movie"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.UpdateMetadata(ctx, ids["showA"], MetadataUpdate{TMDBID: 2, Title: "Show A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.SetProgress(ctx, ids["showA-e1"], 300_000, 1_200_000); err != nil {
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
DROP TABLE channel_programs;
DROP TABLE channels;
PRAGMA user_version = 13`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := Migrate(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if result.From != 13 || result.To != currentSchemaVersion || result.Created {
		t.Fatalf("migration result = %+v", result)
	}
	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = migrated.Close() }()
	var playbackRows, manualArtwork int
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM playback_state`).Scan(&playbackRows); err != nil {
		t.Fatal(err)
	}
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM images WHERE manually_selected = 1`).Scan(&manualArtwork); err != nil {
		t.Fatal(err)
	}
	if playbackRows != 1 || manualArtwork != 1 {
		t.Fatalf("migrated rows = playback %d, manual artwork %d", playbackRows, manualArtwork)
	}
	// Matched shows go back to awaiting details so the next scan stores their
	// genres; an unmatched show has nothing to reload, and movies already
	// carry theirs.
	for name, want := range map[string]bool{"movie1": true, "showA": false, "showB": false} {
		item, err := migrated.Item(ctx, ids[name])
		if err != nil {
			t.Fatal(err)
		}
		if item.DetailsLoaded != want {
			t.Fatalf("%s details loaded = %v, want %v", name, item.DetailsLoaded, want)
		}
	}
}

func TestMigrate14To15AddsChannelTables(t *testing.T) {
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

	// Removing the version-15-only tables recreates the exact version-14 schema
	// shape while retaining representative catalog, playback, and artwork rows.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
DROP TABLE channel_programs;
DROP TABLE channels;
PRAGMA user_version = 14;`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := Migrate(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if result.From != 14 || result.To != currentSchemaVersion || result.Created {
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

	// The new tables are empty and usable: a channel takes a number, and its
	// programs follow the channel out of the catalog.
	channel, err := migrated.CreateChannel(ctx, Channel{
		Key: "show:1", Name: "Show A", Kind: "show", ItemID: ids["showA"],
	}, ChannelTime(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if channel.Number != 1 {
		t.Fatalf("first channel number = %d, want 1", channel.Number)
	}
	if err := migrated.AppendChannelPrograms(ctx, channel.ID, []ScheduledProgram{{
		ItemID: ids["showA-e1"], StartsAt: "2026-08-29T20:00:00Z", EndsAt: "2026-08-29T20:30:00Z",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := migrated.db.ExecContext(ctx, `DELETE FROM channels WHERE id = ?`, channel.ID); err != nil {
		t.Fatal(err)
	}
	var programRows int
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM channel_programs`).Scan(&programRows); err != nil {
		t.Fatal(err)
	}
	if programRows != 0 {
		t.Fatalf("programs left after their channel was removed = %d", programRows)
	}
}

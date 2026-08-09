package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
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

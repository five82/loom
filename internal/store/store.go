package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

// Store owns Loom's durable catalog and playback state.
type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
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
	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > 2 {
		return fmt.Errorf("database schema version %d is newer than supported version 2", version)
	}
	if version == 2 {
		return nil
	}
	if version == 1 {
		return s.migrateImagesV2()
	}
	const schema = `
CREATE TABLE libraries (
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL UNIQUE CHECK (kind IN ('movies', 'tv')),
    name TEXT NOT NULL,
    path TEXT NOT NULL,
    last_scan_id INTEGER
);
CREATE TABLE scan_runs (
    id INTEGER PRIMARY KEY,
    library_id INTEGER NOT NULL REFERENCES libraries(id),
    started_at TEXT NOT NULL,
    finished_at TEXT,
    status TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed')),
    discovered_files INTEGER NOT NULL DEFAULT 0,
    changed_files INTEGER NOT NULL DEFAULT 0,
    probe_errors INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE items (
    id INTEGER PRIMARY KEY,
    library_id INTEGER NOT NULL REFERENCES libraries(id),
    parent_id INTEGER REFERENCES items(id),
    source_key TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('movie', 'show', 'season', 'episode', 'unmatched')),
    title TEXT NOT NULL,
    year INTEGER NOT NULL DEFAULT 0,
    season_number INTEGER NOT NULL DEFAULT 0,
    episode_number INTEGER NOT NULL DEFAULT 0,
    episode_end_number INTEGER NOT NULL DEFAULT 0,
    tmdb_id INTEGER NOT NULL DEFAULT 0,
    overview TEXT NOT NULL DEFAULT '',
    release_date TEXT NOT NULL DEFAULT '',
    available INTEGER NOT NULL DEFAULT 1,
    last_seen_scan_id INTEGER NOT NULL,
    added_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (library_id, source_key)
);
CREATE INDEX items_parent_idx ON items(parent_id, available, title);
CREATE INDEX items_library_idx ON items(library_id, available, kind);
CREATE TABLE media_files (
    id INTEGER PRIMARY KEY,
    item_id INTEGER NOT NULL UNIQUE REFERENCES items(id) ON DELETE CASCADE,
    path TEXT NOT NULL UNIQUE,
    size INTEGER NOT NULL,
    mtime_ns INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    container TEXT NOT NULL DEFAULT '',
    probe_error TEXT NOT NULL DEFAULT '',
    last_seen_scan_id INTEGER NOT NULL
);
CREATE TABLE media_streams (
    id INTEGER PRIMARY KEY,
    media_file_id INTEGER NOT NULL REFERENCES media_files(id) ON DELETE CASCADE,
    stream_index INTEGER NOT NULL,
    kind TEXT NOT NULL,
    codec TEXT NOT NULL DEFAULT '',
    language TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    width INTEGER NOT NULL DEFAULT 0,
    height INTEGER NOT NULL DEFAULT 0,
    channels INTEGER NOT NULL DEFAULT 0,
    is_default INTEGER NOT NULL DEFAULT 0,
    is_forced INTEGER NOT NULL DEFAULT 0,
    UNIQUE (media_file_id, stream_index)
);
CREATE TABLE images (
    id INTEGER PRIMARY KEY,
    item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('poster', 'backdrop')),
    path TEXT NOT NULL UNIQUE,
    source_url TEXT NOT NULL,
    provider TEXT NOT NULL DEFAULT 'tmdb',
    provider_path TEXT NOT NULL DEFAULT '',
    tag TEXT NOT NULL,
    content_type TEXT NOT NULL,
    width INTEGER NOT NULL DEFAULT 0,
    height INTEGER NOT NULL DEFAULT 0,
    manually_selected INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL,
    UNIQUE (item_id, kind)
);
CREATE TABLE playback_state (
    item_id INTEGER PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
    position_ms INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL,
    played INTEGER NOT NULL,
    updated_at TEXT NOT NULL
);
PRAGMA user_version = 2;
`
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin database migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(schema); err != nil {
		return fmt.Errorf("create database schema: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit database schema: %w", err)
	}
	return nil
}

func (s *Store) migrateImagesV2() error {
	const migration = `
ALTER TABLE images ADD COLUMN provider TEXT NOT NULL DEFAULT 'tmdb';
ALTER TABLE images ADD COLUMN provider_path TEXT NOT NULL DEFAULT '';
ALTER TABLE images ADD COLUMN tag TEXT NOT NULL DEFAULT '';
ALTER TABLE images ADD COLUMN content_type TEXT NOT NULL DEFAULT '';
ALTER TABLE images ADD COLUMN width INTEGER NOT NULL DEFAULT 0;
ALTER TABLE images ADD COLUMN height INTEGER NOT NULL DEFAULT 0;
ALTER TABLE images ADD COLUMN manually_selected INTEGER NOT NULL DEFAULT 0;
ALTER TABLE images ADD COLUMN updated_at TEXT NOT NULL DEFAULT '';
UPDATE images SET tag = printf('legacy-%d', id), updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now');
PRAGMA user_version = 2;
`
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin image migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(migration); err != nil {
		return fmt.Errorf("migrate images to schema version 2: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit image migration: %w", err)
	}
	return nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// Library describes one configured library.
type Library struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	ItemCount int64  `json:"item_count"`
}

func (s *Store) EnsureLibrary(ctx context.Context, kind, path string) (int64, error) {
	name := "Movies"
	if kind == "tv" {
		name = "TV"
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `
INSERT INTO libraries(kind, name, path) VALUES (?, ?, ?)
ON CONFLICT(kind) DO UPDATE SET name = excluded.name, path = excluded.path
RETURNING id`, kind, name, path).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("ensure %s library: %w", kind, err)
	}
	return id, nil
}

func (s *Store) Libraries(ctx context.Context) ([]Library, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT l.id, l.kind, l.name, l.path, COUNT(i.id)
FROM libraries l
LEFT JOIN items i ON i.library_id = l.id AND i.available = 1
    AND i.kind IN ('movie', 'show', 'unmatched')
GROUP BY l.id ORDER BY l.id`)
	if err != nil {
		return nil, fmt.Errorf("list libraries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var result []Library
	for rows.Next() {
		var library Library
		if err := rows.Scan(&library.ID, &library.Kind, &library.Name, &library.Path, &library.ItemCount); err != nil {
			return nil, fmt.Errorf("scan library: %w", err)
		}
		result = append(result, library)
	}
	return result, rows.Err()
}

func (s *Store) StartScan(ctx context.Context, kind, path string) (libraryID, scanID int64, err error) {
	libraryID, err = s.EnsureLibrary(ctx, kind, path)
	if err != nil {
		return 0, 0, err
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO scan_runs(library_id, started_at, status) VALUES (?, ?, 'running')`, libraryID, now())
	if err != nil {
		return 0, 0, fmt.Errorf("start scan: %w", err)
	}
	scanID, err = result.LastInsertId()
	if err != nil {
		return 0, 0, fmt.Errorf("read scan ID: %w", err)
	}
	return libraryID, scanID, nil
}

func (s *Store) FinishScan(ctx context.Context, libraryID, scanID int64, discovered, changed, probeErrors int, scanErr error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("finish scan transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	status := "completed"
	errorText := ""
	if scanErr != nil {
		status = "failed"
		errorText = scanErr.Error()
	} else {
		if _, err := tx.ExecContext(ctx, `
UPDATE items SET available = CASE WHEN last_seen_scan_id = ? THEN 1 ELSE 0 END
WHERE library_id = ?`, scanID, libraryID); err != nil {
			return fmt.Errorf("reconcile scan: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE libraries SET last_scan_id = ? WHERE id = ?`, scanID, libraryID); err != nil {
			return fmt.Errorf("record library scan: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE scan_runs SET finished_at = ?, status = ?, discovered_files = ?, changed_files = ?,
    probe_errors = ?, error = ? WHERE id = ?`,
		now(), status, discovered, changed, probeErrors, errorText, scanID); err != nil {
		return fmt.Errorf("record scan result: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit scan result: %w", err)
	}
	return nil
}

// ItemInput is the scanner-owned representation of a catalog item.
type ItemInput struct {
	LibraryID        int64
	ParentID         *int64
	SourceKey        string
	Kind             string
	Title            string
	Year             int
	SeasonNumber     int
	EpisodeNumber    int
	EpisodeEndNumber int
	ScanID           int64
}

func (s *Store) UpsertItem(ctx context.Context, item ItemInput) (int64, error) {
	timestamp := now()
	var id int64
	err := s.db.QueryRowContext(ctx, `
INSERT INTO items(library_id, parent_id, source_key, kind, title, year, season_number,
    episode_number, episode_end_number, available, last_seen_scan_id, added_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
ON CONFLICT(library_id, source_key) DO UPDATE SET
    parent_id = excluded.parent_id,
    kind = excluded.kind,
    title = CASE WHEN items.tmdb_id = 0 THEN excluded.title ELSE items.title END,
    year = CASE WHEN items.tmdb_id = 0 THEN excluded.year ELSE items.year END,
    season_number = excluded.season_number,
    episode_number = excluded.episode_number,
    episode_end_number = excluded.episode_end_number,
    available = 1,
    last_seen_scan_id = excluded.last_seen_scan_id,
    updated_at = excluded.updated_at
RETURNING id`, item.LibraryID, item.ParentID, item.SourceKey, item.Kind, item.Title,
		item.Year, item.SeasonNumber, item.EpisodeNumber, item.EpisodeEndNumber,
		item.ScanID, timestamp, timestamp).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert %s %q: %w", item.Kind, item.Title, err)
	}
	return id, nil
}

// MediaFile is a directly playable source file.
type MediaFile struct {
	ID             int64    `json:"id"`
	ItemID         int64    `json:"item_id"`
	Path           string   `json:"-"`
	Filename       string   `json:"filename"`
	Size           int64    `json:"size"`
	MTimeNS        int64    `json:"-"`
	DurationMS     int64    `json:"duration_ms"`
	Container      string   `json:"container"`
	ProbeError     string   `json:"probe_error,omitempty"`
	LastSeenScanID int64    `json:"-"`
	Streams        []Stream `json:"streams,omitempty"`
}

type Stream struct {
	Index     int    `json:"index"`
	Kind      string `json:"kind"`
	Codec     string `json:"codec"`
	Language  string `json:"language,omitempty"`
	Title     string `json:"title,omitempty"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	Channels  int    `json:"channels,omitempty"`
	IsDefault bool   `json:"is_default"`
	IsForced  bool   `json:"is_forced"`
}

func (s *Store) MediaByPath(ctx context.Context, path string) (*MediaFile, error) {
	var media MediaFile
	err := s.db.QueryRowContext(ctx, `
SELECT id, item_id, path, size, mtime_ns, duration_ms, container, probe_error, last_seen_scan_id
FROM media_files WHERE path = ?`, path).Scan(&media.ID, &media.ItemID, &media.Path,
		&media.Size, &media.MTimeNS, &media.DurationMS, &media.Container, &media.ProbeError,
		&media.LastSeenScanID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find media file: %w", err)
	}
	media.Filename = filepath.Base(media.Path)
	return &media, nil
}

func (s *Store) TouchMedia(ctx context.Context, itemID, scanID int64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE media_files SET last_seen_scan_id = ? WHERE item_id = ?`, scanID, itemID)
	if err != nil {
		return fmt.Errorf("touch media file: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read touched media count: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpsertMedia(ctx context.Context, media MediaFile, streams []Stream) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("upsert media transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var id int64
	err = tx.QueryRowContext(ctx, `
INSERT INTO media_files(item_id, path, size, mtime_ns, duration_ms, container, probe_error, last_seen_scan_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(item_id) DO UPDATE SET
    path = excluded.path,
    size = excluded.size,
    mtime_ns = excluded.mtime_ns,
    duration_ms = excluded.duration_ms,
    container = excluded.container,
    probe_error = excluded.probe_error,
    last_seen_scan_id = excluded.last_seen_scan_id
RETURNING id`, media.ItemID, media.Path, media.Size, media.MTimeNS, media.DurationMS,
		media.Container, media.ProbeError, media.LastSeenScanID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert media file: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM media_streams WHERE media_file_id = ?`, id); err != nil {
		return 0, fmt.Errorf("replace media streams: %w", err)
	}
	for _, stream := range streams {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO media_streams(media_file_id, stream_index, kind, codec, language, title,
    width, height, channels, is_default, is_forced)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, stream.Index, stream.Kind, stream.Codec,
			stream.Language, stream.Title, stream.Width, stream.Height, stream.Channels,
			stream.IsDefault, stream.IsForced); err != nil {
			return 0, fmt.Errorf("insert media stream: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit media file: %w", err)
	}
	return id, nil
}

func (s *Store) AvailableMediaCount(ctx context.Context, libraryID int64) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM media_files m JOIN items i ON i.id = m.item_id
WHERE i.library_id = ? AND i.available = 1`, libraryID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count library media: %w", err)
	}
	return count, nil
}

// Item is the API-facing catalog representation.
type Item struct {
	ID               int64      `json:"id"`
	LibraryID        int64      `json:"library_id"`
	ParentID         *int64     `json:"parent_id,omitempty"`
	Kind             string     `json:"kind"`
	Title            string     `json:"title"`
	Year             int        `json:"year,omitempty"`
	SeasonNumber     int        `json:"season_number,omitempty"`
	EpisodeNumber    int        `json:"episode_number,omitempty"`
	EpisodeEndNumber int        `json:"episode_end_number,omitempty"`
	TMDBID           int64      `json:"tmdb_id,omitempty"`
	Overview         string     `json:"overview,omitempty"`
	ReleaseDate      string     `json:"release_date,omitempty"`
	PosterImageID    int64      `json:"poster_image_id,omitempty"`
	PosterImageTag   string     `json:"poster_image_tag,omitempty"`
	BackdropImageID  int64      `json:"backdrop_image_id,omitempty"`
	BackdropImageTag string     `json:"backdrop_image_tag,omitempty"`
	AddedAt          string     `json:"added_at"`
	UpdatedAt        string     `json:"updated_at"`
	Media            *MediaFile `json:"media,omitempty"`
	Progress         *Progress  `json:"progress,omitempty"`
}

const itemColumns = `i.id, i.library_id, i.parent_id, i.kind, i.title, i.year, i.season_number,
    i.episode_number, i.episode_end_number, i.tmdb_id, i.overview, i.release_date,
    COALESCE(
        (SELECT id FROM images WHERE item_id = i.id AND kind = 'poster'),
        (SELECT id FROM images WHERE item_id = CASE
            WHEN i.kind = 'episode' THEN (SELECT parent_id FROM items WHERE id = i.parent_id)
            WHEN i.kind = 'season' THEN i.parent_id END AND kind = 'poster'), 0),
    COALESCE(
        (SELECT tag FROM images WHERE item_id = i.id AND kind = 'poster'),
        (SELECT tag FROM images WHERE item_id = CASE
            WHEN i.kind = 'episode' THEN (SELECT parent_id FROM items WHERE id = i.parent_id)
            WHEN i.kind = 'season' THEN i.parent_id END AND kind = 'poster'), ''),
    COALESCE(
        (SELECT id FROM images WHERE item_id = i.id AND kind = 'backdrop'),
        (SELECT id FROM images WHERE item_id = CASE
            WHEN i.kind = 'episode' THEN (SELECT parent_id FROM items WHERE id = i.parent_id)
            WHEN i.kind = 'season' THEN i.parent_id END AND kind = 'backdrop'), 0),
    COALESCE(
        (SELECT tag FROM images WHERE item_id = i.id AND kind = 'backdrop'),
        (SELECT tag FROM images WHERE item_id = CASE
            WHEN i.kind = 'episode' THEN (SELECT parent_id FROM items WHERE id = i.parent_id)
            WHEN i.kind = 'season' THEN i.parent_id END AND kind = 'backdrop'), ''),
    i.added_at, i.updated_at`

type ListOptions struct {
	LibraryKind string
	ParentID    *int64
	TopLevel    bool
	Kind        string
	Limit       int
	Offset      int
}

func (s *Store) ListItems(ctx context.Context, opts ListOptions) ([]Item, error) {
	if opts.Limit <= 0 || opts.Limit > 200 {
		opts.Limit = 50
	}
	var clauses = []string{"i.available = 1"}
	var args []any
	if opts.LibraryKind != "" {
		clauses = append(clauses, "l.kind = ?")
		args = append(args, opts.LibraryKind)
	}
	if opts.ParentID != nil {
		clauses = append(clauses, "i.parent_id = ?")
		args = append(args, *opts.ParentID)
	} else if opts.TopLevel {
		clauses = append(clauses, "i.parent_id IS NULL")
	}
	if opts.Kind != "" {
		clauses = append(clauses, "i.kind = ?")
		args = append(args, opts.Kind)
	}
	args = append(args, opts.Limit, opts.Offset)
	query := `SELECT ` + itemColumns + `
FROM items i JOIN libraries l ON l.id = i.library_id
WHERE ` + strings.Join(clauses, " AND ") + `
ORDER BY CASE i.kind WHEN 'season' THEN i.season_number WHEN 'episode' THEN i.episode_number ELSE 0 END,
    i.title COLLATE NOCASE, i.id
LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var result []Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanItem(row rowScanner) (Item, error) {
	var item Item
	var parent sql.NullInt64
	if err := row.Scan(&item.ID, &item.LibraryID, &parent, &item.Kind, &item.Title, &item.Year,
		&item.SeasonNumber, &item.EpisodeNumber, &item.EpisodeEndNumber, &item.TMDBID,
		&item.Overview, &item.ReleaseDate, &item.PosterImageID, &item.PosterImageTag,
		&item.BackdropImageID, &item.BackdropImageTag, &item.AddedAt, &item.UpdatedAt); err != nil {
		return Item{}, fmt.Errorf("scan item: %w", err)
	}
	if parent.Valid {
		item.ParentID = &parent.Int64
	}
	return item, nil
}

func (s *Store) Item(ctx context.Context, id int64) (*Item, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+itemColumns+`
FROM items i WHERE i.id = ? AND i.available = 1`, id)
	item, err := scanItem(row)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(errors.Unwrap(err), sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	media, err := s.mediaForItem(ctx, id)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if err == nil {
		item.Media = media
	}
	progress, err := s.progress(ctx, id)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if err == nil {
		item.Progress = progress
	}
	return &item, nil
}

func (s *Store) mediaForItem(ctx context.Context, itemID int64) (*MediaFile, error) {
	var media MediaFile
	err := s.db.QueryRowContext(ctx, `
SELECT id, item_id, path, size, mtime_ns, duration_ms, container, probe_error, last_seen_scan_id
FROM media_files WHERE item_id = ?`, itemID).Scan(&media.ID, &media.ItemID, &media.Path,
		&media.Size, &media.MTimeNS, &media.DurationMS, &media.Container,
		&media.ProbeError, &media.LastSeenScanID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get item media: %w", err)
	}
	media.Filename = filepath.Base(media.Path)
	streams, err := s.streams(ctx, media.ID)
	if err != nil {
		return nil, err
	}
	media.Streams = streams
	return &media, nil
}

func (s *Store) Media(ctx context.Context, id int64) (*MediaFile, error) {
	var media MediaFile
	err := s.db.QueryRowContext(ctx, `
SELECT m.id, m.item_id, m.path, m.size, m.mtime_ns, m.duration_ms, m.container,
    m.probe_error, m.last_seen_scan_id
FROM media_files m JOIN items i ON i.id = m.item_id
WHERE m.id = ? AND i.available = 1`, id).Scan(&media.ID, &media.ItemID, &media.Path,
		&media.Size, &media.MTimeNS, &media.DurationMS, &media.Container,
		&media.ProbeError, &media.LastSeenScanID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get media: %w", err)
	}
	media.Filename = filepath.Base(media.Path)
	return &media, nil
}

func (s *Store) streams(ctx context.Context, mediaID int64) ([]Stream, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT stream_index, kind, codec, language, title, width, height, channels, is_default, is_forced
FROM media_streams WHERE media_file_id = ? ORDER BY stream_index`, mediaID)
	if err != nil {
		return nil, fmt.Errorf("list media streams: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var streams []Stream
	for rows.Next() {
		var stream Stream
		if err := rows.Scan(&stream.Index, &stream.Kind, &stream.Codec, &stream.Language,
			&stream.Title, &stream.Width, &stream.Height, &stream.Channels,
			&stream.IsDefault, &stream.IsForced); err != nil {
			return nil, fmt.Errorf("scan media stream: %w", err)
		}
		streams = append(streams, stream)
	}
	return streams, rows.Err()
}

// Progress is global because Loom currently has one implicit user.
type Progress struct {
	PositionMS       int64  `json:"position_ms"`
	DurationMS       int64  `json:"duration_ms"`
	Played           bool   `json:"played"`
	ResumePositionMS int64  `json:"resume_position_ms"`
	UpdatedAt        string `json:"updated_at"`
}

func (s *Store) SetProgress(ctx context.Context, itemID, positionMS, durationMS int64) (*Progress, error) {
	if positionMS < 0 || durationMS < 0 {
		return nil, fmt.Errorf("position and duration must be non-negative")
	}
	var mediaDuration int64
	if err := s.db.QueryRowContext(ctx, `SELECT duration_ms FROM media_files WHERE item_id = ?`, itemID).Scan(&mediaDuration); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("find playable item: %w", err)
	}
	if durationMS == 0 {
		durationMS = mediaDuration
	}
	if durationMS <= 0 {
		return nil, fmt.Errorf("duration must be positive")
	}
	if positionMS > durationMS {
		positionMS = durationMS
	}
	played := float64(positionMS)/float64(durationMS) >= 0.90
	timestamp := now()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO playback_state(item_id, position_ms, duration_ms, played, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(item_id) DO UPDATE SET position_ms = excluded.position_ms,
    duration_ms = excluded.duration_ms, played = excluded.played, updated_at = excluded.updated_at`,
		itemID, positionMS, durationMS, played, timestamp)
	if err != nil {
		return nil, fmt.Errorf("save playback progress: %w", err)
	}
	return makeProgress(positionMS, durationMS, played, timestamp), nil
}

func makeProgress(positionMS, durationMS int64, played bool, updatedAt string) *Progress {
	resume := int64(0)
	if !played && durationMS >= 5*60*1000 && float64(positionMS)/float64(durationMS) >= 0.05 {
		resume = positionMS
	}
	return &Progress{
		PositionMS: positionMS, DurationMS: durationMS, Played: played,
		ResumePositionMS: resume, UpdatedAt: updatedAt,
	}
}

func (s *Store) progress(ctx context.Context, itemID int64) (*Progress, error) {
	var position, duration int64
	var played bool
	var updated string
	err := s.db.QueryRowContext(ctx, `
SELECT position_ms, duration_ms, played, updated_at FROM playback_state WHERE item_id = ?`, itemID).
		Scan(&position, &duration, &played, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get playback progress: %w", err)
	}
	return makeProgress(position, duration, played, updated), nil
}

func (s *Store) ContinueWatching(ctx context.Context, limit int) ([]Item, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemColumns+`,
    p.position_ms, p.duration_ms, p.played, p.updated_at
FROM playback_state p JOIN items i ON i.id = p.item_id
WHERE i.available = 1 AND p.played = 0 AND p.duration_ms >= 300000
    AND CAST(p.position_ms AS REAL) / p.duration_ms >= 0.05
    AND CAST(p.position_ms AS REAL) / p.duration_ms < 0.90
ORDER BY p.updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list continue watching: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var result []Item
	for rows.Next() {
		var item Item
		var parent sql.NullInt64
		var position, duration int64
		var played bool
		var updated string
		if err := rows.Scan(&item.ID, &item.LibraryID, &parent, &item.Kind, &item.Title,
			&item.Year, &item.SeasonNumber, &item.EpisodeNumber, &item.EpisodeEndNumber,
			&item.TMDBID, &item.Overview, &item.ReleaseDate, &item.PosterImageID,
			&item.PosterImageTag, &item.BackdropImageID, &item.BackdropImageTag,
			&item.AddedAt, &item.UpdatedAt, &position, &duration, &played, &updated); err != nil {
			return nil, fmt.Errorf("scan continue-watching item: %w", err)
		}
		if parent.Valid {
			item.ParentID = &parent.Int64
		}
		item.Progress = makeProgress(position, duration, played, updated)
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) RecentlyAdded(ctx context.Context, limit int) ([]Item, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemColumns+`
FROM items i WHERE i.available = 1 AND i.kind IN ('movie', 'episode', 'unmatched')
ORDER BY i.added_at DESC, i.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list recently added: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var result []Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

type Stats struct {
	Movies    int `json:"movies"`
	Shows     int `json:"shows"`
	Episodes  int `json:"episodes"`
	Unmatched int `json:"unmatched"`
	Media     int `json:"media_files"`
}

func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var stats Stats
	rows, err := s.db.QueryContext(ctx, `
SELECT kind, COUNT(*) FROM items WHERE available = 1 GROUP BY kind`)
	if err != nil {
		return stats, fmt.Errorf("catalog stats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var kind string
		var count int
		if err := rows.Scan(&kind, &count); err != nil {
			return stats, err
		}
		switch kind {
		case "movie":
			stats.Movies = count
		case "show":
			stats.Shows = count
		case "episode":
			stats.Episodes = count
		}
	}
	if err := rows.Err(); err != nil {
		return stats, err
	}
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM items
WHERE available = 1 AND (kind = 'unmatched' OR (kind IN ('movie', 'show') AND tmdb_id = 0))`).
		Scan(&stats.Unmatched); err != nil {
		return stats, fmt.Errorf("unmatched stats: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM media_files m JOIN items i ON i.id = m.item_id WHERE i.available = 1`).Scan(&stats.Media); err != nil {
		return stats, fmt.Errorf("media stats: %w", err)
	}
	return stats, nil
}

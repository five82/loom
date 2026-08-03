package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type Genre struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// MetadataUpdate applies provider-owned fields to one catalog item.
type MetadataUpdate struct {
	TMDBID       int64
	Title        string
	Year         int
	Overview     string
	ReleaseDate  string
	Genres       []Genre
	GenresLoaded bool
}

type contextExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *Store) UpdateMetadata(ctx context.Context, itemID int64, metadata MetadataUpdate) error {
	if !metadata.GenresLoaded {
		return updateMetadata(ctx, s.db, itemID, metadata)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("update metadata transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := updateMetadata(ctx, tx, itemID, metadata); err != nil {
		return err
	}
	if err := replaceGenres(ctx, tx, itemID, metadata.Genres); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit metadata update: %w", err)
	}
	return nil
}

func updateMetadata(ctx context.Context, db contextExecer, itemID int64, metadata MetadataUpdate) error {
	result, err := db.ExecContext(ctx, `
UPDATE items SET tmdb_id = ?, title = CASE WHEN ? = '' THEN title ELSE ? END,
    year = CASE WHEN ? = 0 THEN year ELSE ? END, overview = ?, release_date = ?,
    genres_loaded = CASE WHEN ? THEN 1 ELSE genres_loaded END, updated_at = ?
WHERE id = ? AND available = 1`, metadata.TMDBID, metadata.Title, metadata.Title,
		metadata.Year, metadata.Year, metadata.Overview, metadata.ReleaseDate,
		metadata.GenresLoaded, now(), itemID)
	if err != nil {
		return fmt.Errorf("update item metadata: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read updated metadata count: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateGenres(ctx context.Context, itemID int64, genres []Genre) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("update genres transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
UPDATE items SET genres_loaded = 1, updated_at = ?
WHERE id = ? AND available = 1 AND kind = 'movie'`, now(), itemID)
	if err != nil {
		return fmt.Errorf("mark movie genres loaded: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read updated genre item count: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	if err := replaceGenres(ctx, tx, itemID, genres); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit genre update: %w", err)
	}
	return nil
}

func replaceGenres(ctx context.Context, tx *sql.Tx, itemID int64, genres []Genre) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM item_genres WHERE item_id = ?`, itemID); err != nil {
		return fmt.Errorf("clear item genres: %w", err)
	}
	for _, genre := range genres {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO genres(id, name) VALUES (?, ?)
ON CONFLICT(id) DO UPDATE SET name = excluded.name`, genre.ID, genre.Name); err != nil {
			return fmt.Errorf("upsert genre %d: %w", genre.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO item_genres(item_id, genre_id) VALUES (?, ?)`, itemID, genre.ID); err != nil {
			return fmt.Errorf("associate item genre %d: %w", genre.ID, err)
		}
	}
	return nil
}

type GenreSummary struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	ItemCount int    `json:"item_count"`
}

func (s *Store) Genres(ctx context.Context) ([]GenreSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT g.id, g.name, COUNT(*)
FROM genres g
JOIN item_genres ig ON ig.genre_id = g.id
JOIN items i ON i.id = ig.item_id
WHERE i.available = 1 AND i.kind = 'movie'
GROUP BY g.id, g.name
ORDER BY g.name COLLATE NOCASE, g.id`)
	if err != nil {
		return nil, fmt.Errorf("list genres: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var genres []GenreSummary
	for rows.Next() {
		var genre GenreSummary
		if err := rows.Scan(&genre.ID, &genre.Name, &genre.ItemCount); err != nil {
			return nil, fmt.Errorf("scan genre: %w", err)
		}
		genres = append(genres, genre)
	}
	return genres, rows.Err()
}

func (s *Store) SeasonsForShow(ctx context.Context, showID int64) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemColumns+`
FROM items i
WHERE i.parent_id = ? AND i.available = 1 AND i.kind = 'season'
ORDER BY i.season_number`, showID)
	if err != nil {
		return nil, fmt.Errorf("list show seasons: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var items []Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) EpisodesForShow(ctx context.Context, showID int64) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemColumns+`
FROM items i JOIN items season ON season.id = i.parent_id
WHERE season.parent_id = ? AND i.available = 1 AND i.kind = 'episode'
ORDER BY i.season_number, i.episode_number`, showID)
	if err != nil {
		return nil, fmt.Errorf("list show episodes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var items []Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) UnmatchedItems(ctx context.Context) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemColumns+`
FROM items i
WHERE i.available = 1 AND (i.kind = 'unmatched' OR (i.kind IN ('movie', 'show') AND i.tmdb_id = 0))
ORDER BY i.kind, i.title COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("list unmatched items: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var items []Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type Image struct {
	ID               int64  `json:"id"`
	ItemID           int64  `json:"item_id"`
	Kind             string `json:"kind"`
	Path             string `json:"-"`
	SourceURL        string `json:"-"`
	Provider         string `json:"provider"`
	ProviderPath     string `json:"provider_path,omitempty"`
	Tag              string `json:"tag"`
	ContentType      string `json:"content_type,omitempty"`
	Width            int    `json:"width,omitempty"`
	Height           int    `json:"height,omitempty"`
	ManuallySelected bool   `json:"manually_selected"`
	UpdatedAt        string `json:"updated_at"`
}

func (s *Store) UpsertImage(ctx context.Context, image Image) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
INSERT INTO images(item_id, kind, path, source_url, provider, provider_path, tag,
    content_type, width, height, manually_selected, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(item_id, kind) DO UPDATE SET path = excluded.path, source_url = excluded.source_url,
    provider = excluded.provider, provider_path = excluded.provider_path, tag = excluded.tag,
    content_type = excluded.content_type, width = excluded.width, height = excluded.height,
    manually_selected = excluded.manually_selected, updated_at = excluded.updated_at
RETURNING id`, image.ItemID, image.Kind, image.Path, image.SourceURL, image.Provider,
		image.ProviderPath, image.Tag, image.ContentType, image.Width, image.Height,
		image.ManuallySelected, image.UpdatedAt).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert %s image: %w", image.Kind, err)
	}
	return id, nil
}

const imageColumns = `images.id, images.item_id, images.kind, images.path, images.source_url,
    images.provider, images.provider_path, images.tag, images.content_type, images.width,
    images.height, images.manually_selected, images.updated_at`

func scanImage(row rowScanner) (*Image, error) {
	var image Image
	if err := row.Scan(&image.ID, &image.ItemID, &image.Kind, &image.Path, &image.SourceURL,
		&image.Provider, &image.ProviderPath, &image.Tag, &image.ContentType, &image.Width,
		&image.Height, &image.ManuallySelected, &image.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan image: %w", err)
	}
	return &image, nil
}

func (s *Store) Image(ctx context.Context, id int64) (*Image, error) {
	return scanImage(s.db.QueryRowContext(ctx, `SELECT `+imageColumns+`
FROM images JOIN items ON items.id = images.item_id
WHERE images.id = ? AND items.available = 1`, id))
}

func (s *Store) ItemImage(ctx context.Context, itemID int64, kind string) (*Image, error) {
	return scanImage(s.db.QueryRowContext(ctx, `SELECT `+imageColumns+`
FROM images JOIN items ON items.id = images.item_id
WHERE images.item_id = ? AND images.kind = ? AND items.available = 1`, itemID, kind))
}

func (s *Store) DeleteItemImage(ctx context.Context, itemID int64, kind string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM images WHERE item_id = ? AND kind = ?`, itemID, kind)
	if err != nil {
		return fmt.Errorf("delete %s image: %w", kind, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read deleted image count: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

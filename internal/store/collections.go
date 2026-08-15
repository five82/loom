package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ItemsByTMDBID returns the available movies carrying the given TMDB ids, in
// release order. Ids with nothing behind them are absent from the result rather
// than an error, which is how a collection member that is not owned disappears
// from its shelf.
func (s *Store) ItemsByTMDBID(ctx context.Context, tmdbIDs []int64) ([]Item, error) {
	if len(tmdbIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(tmdbIDs))
	args := make([]any, len(tmdbIDs))
	for index, tmdbID := range tmdbIDs {
		placeholders[index] = "?"
		args[index] = tmdbID
	}
	return s.collectionItems(ctx, "i.tmdb_id IN ("+strings.Join(placeholders, ",")+")", args,
		releaseOrder)
}

// ItemsByVideoDynamicRange returns available movies whose video stream has one
// of the requested scanner classifications, in alphabetical order. A movie
// appears only once even if its file has multiple matching video streams.
func (s *Store) ItemsByVideoDynamicRange(ctx context.Context, dynamicRanges []string) ([]Item, error) {
	if len(dynamicRanges) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(dynamicRanges))
	args := make([]any, len(dynamicRanges))
	for index, dynamicRange := range dynamicRanges {
		placeholders[index] = "?"
		args[index] = dynamicRange
	}
	predicate := `EXISTS (
        SELECT 1 FROM media_files m
        JOIN media_streams stream ON stream.media_file_id = m.id
        WHERE m.item_id = i.id AND stream.kind = 'video'
            AND stream.dynamic_range IN (` + strings.Join(placeholders, ",") + `)
    )`
	return s.collectionItems(ctx, predicate, args, alphabeticalOrder)
}

// ItemsReleasedBetween returns available movies released inside the inclusive
// date range, newest first.
func (s *Store) ItemsReleasedBetween(ctx context.Context, start, end time.Time) ([]Item, error) {
	if end.Before(start) {
		return nil, nil
	}
	return s.collectionItems(ctx, "i.release_date >= ? AND i.release_date <= ?",
		[]any{start.Format(time.DateOnly), end.Format(time.DateOnly)}, newestReleaseOrder)
}

const releaseOrder = `CASE WHEN i.release_date = '' THEN 1 ELSE 0 END, i.release_date,
    i.year, i.title COLLATE NOCASE, i.id`

const alphabeticalOrder = `CASE
        WHEN substr(i.title, 1, 4) = 'the ' COLLATE NOCASE THEN substr(i.title, 5)
        WHEN substr(i.title, 1, 3) = 'an ' COLLATE NOCASE THEN substr(i.title, 4)
        WHEN substr(i.title, 1, 2) = 'a ' COLLATE NOCASE THEN substr(i.title, 3)
        ELSE i.title
    END COLLATE NOCASE, i.title COLLATE NOCASE, i.id`

const newestReleaseOrder = `i.release_date DESC, i.title COLLATE NOCASE, i.id`

func (s *Store) collectionItems(ctx context.Context, predicate string, args []any, orderBy string) ([]Item, error) {
	// Collections contain movies from both the movies and shorts libraries. TV
	// episodes stay out because their posters and playback semantics do not fit a
	// movie shelf. Playback state rides along so clients can draw watched markers
	// without a request per item.
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemColumns+`,
    COALESCE(p.position_ms, 0), COALESCE(p.duration_ms, 0), COALESCE(p.played, 0),
    COALESCE(p.updated_at, '')
FROM items i
LEFT JOIN playback_state p ON p.item_id = i.id
WHERE i.available = 1 AND i.kind = 'movie' AND `+predicate+`
ORDER BY `+orderBy, args...)
	if err != nil {
		return nil, fmt.Errorf("list collection items: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var result []Item
	for rows.Next() {
		var position, duration int64
		var played bool
		var updated string
		item, err := scanItemFields(rows, &position, &duration, &played, &updated)
		if err != nil {
			return nil, err
		}
		if updated != "" {
			item.Progress = makeProgress(position, duration, played, updated)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := s.populateGenres(ctx, result); err != nil {
		return nil, err
	}
	return result, nil
}

package store

import (
	"context"
	"fmt"
	"strings"
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
	// Restricted to movies because episodes carry TMDB episode ids, which are
	// numbered independently of movie ids and would otherwise collide. Playback
	// state rides along for the same reason browse listings carry it: a shelf
	// draws watched markers without a request per movie.
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemColumns+`,
    COALESCE(p.position_ms, 0), COALESCE(p.duration_ms, 0), COALESCE(p.played, 0),
    COALESCE(p.updated_at, '')
FROM items i
LEFT JOIN playback_state p ON p.item_id = i.id
WHERE i.available = 1 AND i.kind = 'movie'
    AND i.tmdb_id IN (`+strings.Join(placeholders, ",")+`)
ORDER BY CASE WHEN i.release_date = '' THEN 1 ELSE 0 END, i.release_date,
    i.year, i.title COLLATE NOCASE, i.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list items by tmdb id: %w", err)
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

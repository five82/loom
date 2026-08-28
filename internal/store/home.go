package store

import (
	"context"
	"fmt"
)

// DiscoveryLibrary returns every available movie in the movies library and
// every available show, with playback state and genres attached, so the home
// screen's rotating shelves can be drawn from the whole catalog. Shorts stay
// out: a five-minute film in "Something Different" reads as a mistake. Rows
// come back in id order because the shelves shuffle them with a seeded
// generator, and a stable input order is what makes the day's shelves stable.
func (s *Store) DiscoveryLibrary(ctx context.Context) (movies, shows []Item, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemColumns+`,
    COALESCE(p.position_ms, 0), COALESCE(p.duration_ms, 0), COALESCE(p.played, 0),
    COALESCE(p.updated_at, '')
FROM items i JOIN libraries l ON l.id = i.library_id
LEFT JOIN playback_state p ON p.item_id = i.id
WHERE i.available = 1 AND i.parent_id IS NULL
    AND ((i.kind = 'movie' AND l.kind = 'movies') OR (i.kind = 'show' AND l.kind = 'tv'))
ORDER BY i.id`)
	if err != nil {
		return nil, nil, fmt.Errorf("list discovery library: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var all []Item
	for rows.Next() {
		var position, duration int64
		var played bool
		var updated string
		item, err := scanItemFields(rows, &position, &duration, &played, &updated)
		if err != nil {
			return nil, nil, err
		}
		if updated != "" {
			item.Progress = makeProgress(position, duration, played, updated)
		}
		all = append(all, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	if err := s.populateGenres(ctx, all); err != nil {
		return nil, nil, err
	}
	for _, item := range all {
		if item.Kind == "movie" {
			movies = append(movies, item)
		} else {
			shows = append(shows, item)
		}
	}
	return movies, shows, nil
}

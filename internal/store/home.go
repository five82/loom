package store

import (
	"context"
	"fmt"
)

// DiscoveryLibrary returns every available movie in the movies library, every
// available show, and every available short, with playback state and genres
// attached, so the home screen's rotating shelves can be drawn from the whole
// catalog. Shorts come back separately because they only belong on a shelf of
// their own: a five-minute film in "Something Different" reads as a mistake.
// Rows come back in id order because the shelves shuffle them with a seeded
// generator, and a stable input order is what makes the day's shelves stable.
func (s *Store) DiscoveryLibrary(ctx context.Context) (movies, shows, shorts []Item, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemColumns+`, l.kind,
    COALESCE(p.position_ms, 0), COALESCE(p.duration_ms, 0), COALESCE(p.played, 0),
    COALESCE(p.updated_at, '')
FROM items i JOIN libraries l ON l.id = i.library_id
LEFT JOIN playback_state p ON p.item_id = i.id
WHERE i.available = 1 AND i.parent_id IS NULL
    AND ((i.kind = 'movie' AND l.kind IN ('movies', 'shorts')) OR (i.kind = 'show' AND l.kind = 'tv'))
ORDER BY i.id`)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("list discovery library: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var all []Item
	var libraryKinds []string
	for rows.Next() {
		var libraryKind, updated string
		var position, duration int64
		var played bool
		item, err := scanItemFields(rows, &libraryKind, &position, &duration, &played, &updated)
		if err != nil {
			return nil, nil, nil, err
		}
		if updated != "" {
			item.Progress = makeProgress(position, duration, played, updated)
		}
		all = append(all, item)
		libraryKinds = append(libraryKinds, libraryKind)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, nil, nil, err
	}
	if err := s.populateGenres(ctx, all); err != nil {
		return nil, nil, nil, err
	}
	for index, item := range all {
		switch {
		case item.Kind == "show":
			shows = append(shows, item)
		case libraryKinds[index] == "shorts":
			shorts = append(shorts, item)
		default:
			movies = append(movies, item)
		}
	}
	return movies, shows, shorts, nil
}

// PersonCredit is one headline credit on a movie: a director, or one of the
// three top-billed actors. Supporting cast is left out because a shelf named
// for a person should hold films that person is the reason to watch.
type PersonCredit struct {
	PersonID int64
	Name     string
	Role     string
	ItemID   int64
}

// HeadlineCredits lists the director and top-three-billed actor credits of
// every available movie in the movies library, so the home screen can build a
// "Starring" or "Directed by" shelf for anyone well represented here. Credits
// are stored directors first, so billing is the rank among actor rows alone.
func (s *Store) HeadlineCredits(ctx context.Context) ([]PersonCredit, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT c.person_id, p.name, c.role, c.item_id
FROM item_credits c
JOIN people p ON p.id = c.person_id
JOIN items i ON i.id = c.item_id
JOIN libraries l ON l.id = i.library_id
WHERE i.available = 1 AND i.kind = 'movie' AND l.kind = 'movies'
    AND c.role IN ('director', 'actor')
ORDER BY c.item_id, c.ordering, c.person_id`)
	if err != nil {
		return nil, fmt.Errorf("list headline credits: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var result []PersonCredit
	var currentItem int64
	billed := 0
	for rows.Next() {
		var credit PersonCredit
		if err := rows.Scan(&credit.PersonID, &credit.Name, &credit.Role, &credit.ItemID); err != nil {
			return nil, fmt.Errorf("scan headline credit: %w", err)
		}
		if credit.ItemID != currentItem {
			currentItem = credit.ItemID
			billed = 0
		}
		if credit.Role == "actor" {
			billed++
			if billed > 3 {
				continue
			}
		}
		result = append(result, credit)
	}
	return result, rows.Err()
}

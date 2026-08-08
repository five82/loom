package collections

import "testing"

// The shelves are hand-maintained, so the mistakes worth catching are the ones a
// hand makes: a slug pasted twice, a movie listed twice inside one shelf, a
// truncated id.
func TestDefinitionsAreWellFormed(t *testing.T) {
	slugs := make(map[string]bool, len(All))
	for _, collection := range All {
		if collection.Slug == "" || collection.Title == "" {
			t.Errorf("collection %+v is missing a slug or title", collection)
			continue
		}
		if slugs[collection.Slug] {
			t.Errorf("duplicate slug %q", collection.Slug)
		}
		slugs[collection.Slug] = true
		if len(collection.TMDBIDs) < 2 {
			t.Errorf("collection %q lists %d members; a shelf needs at least two",
				collection.Slug, len(collection.TMDBIDs))
		}
		seen := make(map[int64]bool, len(collection.TMDBIDs))
		for _, tmdbID := range collection.TMDBIDs {
			if tmdbID <= 0 {
				t.Errorf("collection %q has a non-positive TMDB id %d", collection.Slug, tmdbID)
			}
			if seen[tmdbID] {
				t.Errorf("collection %q lists TMDB id %d twice", collection.Slug, tmdbID)
			}
			seen[tmdbID] = true
		}
	}
}

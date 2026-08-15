package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// FeaturedRatingThreshold leaves enough of the current movie library to run
// for about six weeks at two picks per day while still making "highly rated" a
// meaningful distinction.
const FeaturedRatingThreshold = 7.5

// FeaturedPick is the movie selected for one server-local 6am-to-6pm or
// 6pm-to-6am period. Times are returned as UTC instants like Loom's other API
// timestamps, even though their boundaries are calculated in server time.
type FeaturedPick struct {
	Item     Item   `json:"item"`
	StartsAt string `json:"starts_at"`
	EndsAt   string `json:"ends_at"`
}

const featuredEligibility = `i.available = 1 AND i.kind = 'movie' AND l.kind = 'movies'
    AND i.vote_average >= ?
    AND NOT EXISTS (
        SELECT 1 FROM item_genres ig JOIN genres g ON g.id = ig.genre_id
        WHERE ig.item_id = i.id AND g.name = 'Documentary' COLLATE NOCASE
    )`

// featuredPeriod returns the active period in at's location. Constructing both
// boundaries as local clock times, rather than adding twelve hours, preserves
// the requested 6am and 6pm changes across daylight-saving transitions.
func featuredPeriod(at time.Time) (time.Time, time.Time) {
	year, month, day := at.Date()
	location := at.Location()
	if at.Hour() < 6 {
		start := time.Date(year, month, day-1, 18, 0, 0, 0, location)
		end := time.Date(year, month, day, 6, 0, 0, 0, location)
		return start, end
	}
	if at.Hour() < 18 {
		start := time.Date(year, month, day, 6, 0, 0, 0, location)
		end := time.Date(year, month, day, 18, 0, 0, 0, location)
		return start, end
	}
	start := time.Date(year, month, day, 18, 0, 0, 0, location)
	end := time.Date(year, month, day+1, 6, 0, 0, 0, location)
	return start, end
}

// NextFeaturedPickTime returns the next 6am or 6pm boundary in at's location.
func NextFeaturedPickTime(at time.Time) time.Time {
	_, end := featuredPeriod(at)
	return end
}

// FeaturedPickAt returns the active pick, advancing it only when at has entered
// a new server-local period. The database transaction makes simultaneous client
// requests at a boundary observe the same selection.
func (s *Store) FeaturedPickAt(ctx context.Context, at time.Time) (*FeaturedPick, error) {
	start, end := featuredPeriod(at)
	periodStartedAt := start.UTC().Format(time.RFC3339Nano)

	itemID, err := s.featuredItemID(ctx, periodStartedAt)
	if err != nil {
		return nil, err
	}
	item, err := s.Item(ctx, itemID)
	if errors.Is(err, ErrNotFound) {
		// A scan can commit after selection and before Item reads the row. Retry
		// once so that scan reconciliation can supply its replacement.
		itemID, err = s.featuredItemID(ctx, periodStartedAt)
		if err != nil {
			return nil, err
		}
		item, err = s.Item(ctx, itemID)
	}
	if err != nil {
		return nil, err
	}
	return &FeaturedPick{
		Item:     *item,
		StartsAt: start.UTC().Format(time.RFC3339Nano),
		EndsAt:   end.UTC().Format(time.RFC3339Nano),
	}, nil
}

func (s *Store) featuredItemID(ctx context.Context, periodStartedAt string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("featured pick transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var currentID int64
	var currentPeriod string
	err = tx.QueryRowContext(ctx, `
SELECT fp.item_id, fp.period_started_at
FROM featured_pick fp JOIN items i ON i.id = fp.item_id
WHERE fp.singleton = 1 AND i.available = 1`).Scan(&currentID, &currentPeriod)
	if err == nil && currentPeriod == periodStartedAt {
		if err := tx.Commit(); err != nil {
			return 0, fmt.Errorf("commit featured pick: %w", err)
		}
		return currentID, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("read featured pick: %w", err)
	}

	itemID, found, err := selectFeaturedPick(ctx, tx, periodStartedAt, currentID)
	if err != nil {
		return 0, err
	}
	if !found {
		if _, err := tx.ExecContext(ctx, `DELETE FROM featured_pick WHERE singleton = 1`); err != nil {
			return 0, fmt.Errorf("clear featured pick: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return 0, fmt.Errorf("commit empty featured pick: %w", err)
		}
		return 0, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit featured pick: %w", err)
	}
	return itemID, nil
}

// reconcileFeaturedRotation makes the durable cycle match the available,
// eligible movie library. Existing shown flags are retained, so a scan cannot
// reshuffle the cycle or change its current pick. New movies enter the unshown
// part of the cycle and removed movies disappear from it.
func reconcileFeaturedRotation(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
DELETE FROM featured_rotation
WHERE item_id NOT IN (
    SELECT i.id FROM items i JOIN libraries l ON l.id = i.library_id
    WHERE `+featuredEligibility+`
)`, FeaturedRatingThreshold); err != nil {
		return fmt.Errorf("remove ineligible featured movies: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO featured_rotation(item_id, shown)
SELECT i.id, 0 FROM items i JOIN libraries l ON l.id = i.library_id
WHERE `+featuredEligibility+`
ON CONFLICT(item_id) DO NOTHING`, FeaturedRatingThreshold); err != nil {
		return fmt.Errorf("add eligible featured movies: %w", err)
	}
	return nil
}

// replaceUnavailableFeaturedPick is called in the successful scan transaction,
// after availability has been reconciled. It is the sole exception to boundary
// changes: a removed current movie is replaced immediately without resetting
// the period's scheduled end.
func replaceUnavailableFeaturedPick(ctx context.Context, tx *sql.Tx) error {
	var currentID int64
	var periodStartedAt string
	err := tx.QueryRowContext(ctx, `
SELECT fp.item_id, fp.period_started_at
FROM featured_pick fp JOIN items i ON i.id = fp.item_id
WHERE fp.singleton = 1 AND i.available = 0`).Scan(&currentID, &periodStartedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check featured pick availability: %w", err)
	}
	_, found, err := selectFeaturedPick(ctx, tx, periodStartedAt, currentID)
	if err != nil {
		return err
	}
	if !found {
		if _, err := tx.ExecContext(ctx, `DELETE FROM featured_pick WHERE singleton = 1`); err != nil {
			return fmt.Errorf("clear unavailable featured pick: %w", err)
		}
	}
	return nil
}

// selectFeaturedPick chooses uniformly from movies not yet shown in this cycle.
// Once all have appeared it clears the shown flags and starts another random
// cycle, avoiding a back-to-back repeat at the cycle boundary when possible.
func selectFeaturedPick(
	ctx context.Context, tx *sql.Tx, periodStartedAt string, previousID int64,
) (int64, bool, error) {
	var itemID int64
	err := tx.QueryRowContext(ctx, `
SELECT item_id FROM featured_rotation WHERE shown = 0 ORDER BY random() LIMIT 1`).Scan(&itemID)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `UPDATE featured_rotation SET shown = 0`); err != nil {
			return 0, false, fmt.Errorf("restart featured rotation: %w", err)
		}
		err = tx.QueryRowContext(ctx, `
SELECT item_id FROM featured_rotation
WHERE item_id <> ?
ORDER BY random() LIMIT 1`, previousID).Scan(&itemID)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRowContext(ctx, `
SELECT item_id FROM featured_rotation ORDER BY random() LIMIT 1`).Scan(&itemID)
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("select featured movie: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE featured_rotation SET shown = 1 WHERE item_id = ?`, itemID); err != nil {
		return 0, false, fmt.Errorf("mark featured movie shown: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO featured_pick(singleton, item_id, period_started_at) VALUES (1, ?, ?)
ON CONFLICT(singleton) DO UPDATE SET
    item_id = excluded.item_id, period_started_at = excluded.period_started_at`,
		itemID, periodStartedAt); err != nil {
		return 0, false, fmt.Errorf("save featured pick: %w", err)
	}
	return itemID, true, nil
}

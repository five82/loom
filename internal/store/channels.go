package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Channel program times are whole-second RFC 3339 in UTC. Unlike the catalog's
// RFC3339Nano timestamps these are fixed width, which is what lets SQLite order
// and compare them as text, and the API returns the stored strings unchanged.
func ChannelTime(at time.Time) string {
	return at.UTC().Truncate(time.Second).Format(time.RFC3339)
}

// ParseChannelTime reads a stored program boundary back.
func ParseChannelTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse channel time %q: %w", value, err)
	}
	return parsed.UTC(), nil
}

// Channel is one lineup entry as the catalog holds it. A number is assigned
// when a key first appears and is never reused, so a channel a viewer has
// learned keeps its place even as the lineup changes around it.
type Channel struct {
	ID     int64  `json:"id"`
	Number int    `json:"number"`
	Key    string `json:"key"`
	Name   string `json:"name"`
}

// ChannelItem is a schedulable item: what laying out a program needs and
// nothing more.
type ChannelItem struct {
	ItemID     int64
	DurationMS int64
}

// ChannelCursor is a source's place in its run: the last item it aired, the
// cycle that item aired in, and when that cycle began, which is what lets a
// shuffled source skip what another block of its channel has aired since.
type ChannelCursor struct {
	Source         string
	Cycle          int
	CycleStartedAt string
	ItemID         int64
}

// AiredProgram is one program in a channel's history: enough to tell whether
// an item has aired since a moment.
type AiredProgram struct {
	ItemID   int64
	StartsAt string
}

// ScheduledProgram is one program about to be written to a channel.
type ScheduledProgram struct {
	ItemID   int64
	StartsAt string
	EndsAt   string
}

// ChannelProgram is one scheduled program with everything the lineup endpoint
// reports about it. Item carries the same shape as a browse listing.
type ChannelProgram struct {
	ID        int64
	ChannelID int64
	StartsAt  string
	EndsAt    string
	Item      Item
	// Video is the file's first video stream, absent when nothing was probed.
	Video *Stream
	// MediaID and MediaPath let the caller stat the file and build a stream URL
	// the media endpoint accepts. Both are zero when the item has no file.
	MediaID   int64
	MediaPath string
}

// channelEligibility is what every channel can schedule: an available item
// with a file long enough to occupy a slot.
const channelEligibility = `i.available = 1 AND m.duration_ms > 0`

func (s *Store) Channels(ctx context.Context) ([]Channel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, number, key, name FROM channels ORDER BY number`)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]Channel, 0)
	for rows.Next() {
		var channel Channel
		if err := rows.Scan(&channel.ID, &channel.Number, &channel.Key, &channel.Name); err != nil {
			return nil, fmt.Errorf("scan channel: %w", err)
		}
		result = append(result, channel)
	}
	return result, rows.Err()
}

// CreateChannel adds a channel at the next unused number. Numbers are never
// reused, so a channel that leaves the lineup does not hand its number to a
// later one.
func (s *Store) CreateChannel(ctx context.Context, key, name, createdAt string) (Channel, error) {
	channel := Channel{Key: key, Name: name}
	err := s.db.QueryRowContext(ctx, `
INSERT INTO channels(number, key, name, created_at)
VALUES ((SELECT COALESCE(MAX(number), 0) + 1 FROM channels), ?, ?, ?)
RETURNING id, number`, key, name, createdAt).Scan(&channel.ID, &channel.Number)
	if err != nil {
		return Channel{}, fmt.Errorf("create channel %q: %w", key, err)
	}
	return channel, nil
}

// RenameChannel follows the lineup when a channel is renamed.
func (s *Store) RenameChannel(ctx context.Context, id int64, name string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE channels SET name = ? WHERE id = ?`, name, id); err != nil {
		return fmt.Errorf("rename channel %d: %w", id, err)
	}
	return nil
}

// DeleteChannel drops a channel the lineup no longer names, with its programs
// and cursors.
func (s *Store) DeleteChannel(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM channels WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete channel %d: %w", id, err)
	}
	return nil
}

// ChannelShowEpisodes lists a show's airable episodes in the order a channel
// runs them: seasons in order, then the specials.
func (s *Store) ChannelShowEpisodes(ctx context.Context, tmdbID int64) ([]ChannelItem, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT i.id, m.duration_ms
FROM items i
JOIN media_files m ON m.item_id = i.id
JOIN items season ON season.id = i.parent_id
JOIN items show ON show.id = season.parent_id
WHERE `+channelEligibility+` AND i.kind = 'episode'
    AND show.kind = 'show' AND show.available = 1 AND show.tmdb_id = ?
ORDER BY i.season_number = 0, i.season_number, i.episode_number, i.id`, tmdbID)
	if err != nil {
		return nil, fmt.Errorf("list channel episodes of show %d: %w", tmdbID, err)
	}
	return scanChannelItems(rows)
}

// ChannelTitles lists the airable movies carrying the given TMDB ids, in the
// order the ids are given. Ids with nothing behind them are simply absent.
func (s *Store) ChannelTitles(ctx context.Context, tmdbIDs []int64) ([]ChannelItem, error) {
	if len(tmdbIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(tmdbIDs))
	args := make([]any, len(tmdbIDs))
	for index, tmdbID := range tmdbIDs {
		placeholders[index] = "?"
		args[index] = tmdbID
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT i.id, m.duration_ms, i.tmdb_id
FROM items i JOIN media_files m ON m.item_id = i.id
WHERE `+channelEligibility+` AND i.kind = 'movie' AND i.tmdb_id IN (`+strings.Join(placeholders, ",")+`)
ORDER BY i.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list channel titles: %w", err)
	}
	defer func() { _ = rows.Close() }()
	byTMDB := make(map[int64][]ChannelItem)
	for rows.Next() {
		var item ChannelItem
		var tmdbID int64
		if err := rows.Scan(&item.ItemID, &item.DurationMS, &tmdbID); err != nil {
			return nil, fmt.Errorf("scan channel title: %w", err)
		}
		byTMDB[tmdbID] = append(byTMDB[tmdbID], item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var result []ChannelItem
	for _, tmdbID := range tmdbIDs {
		result = append(result, byTMDB[tmdbID]...)
	}
	return result, nil
}

// ChannelMovies lists the airable movies from the movies library carrying any
// of the genres, optionally limited to the given content ratings. The order is
// by id; a genre pool is always shuffled.
func (s *Store) ChannelMovies(ctx context.Context, genres, ratings []string) ([]ChannelItem, error) {
	if len(genres) == 0 {
		return nil, nil
	}
	var args []any
	genrePlaceholders := make([]string, len(genres))
	for index, genre := range genres {
		genrePlaceholders[index] = "?"
		args = append(args, genre)
	}
	ratingPredicate := ""
	if len(ratings) > 0 {
		ratingPlaceholders := make([]string, len(ratings))
		for index, rating := range ratings {
			ratingPlaceholders[index] = "?"
			args = append(args, rating)
		}
		ratingPredicate = ` AND i.content_rating IN (` + strings.Join(ratingPlaceholders, ",") + `)`
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT i.id, m.duration_ms
FROM items i
JOIN media_files m ON m.item_id = i.id
JOIN libraries l ON l.id = i.library_id
WHERE `+channelEligibility+` AND i.kind = 'movie' AND l.kind = 'movies'
    AND EXISTS (SELECT 1 FROM item_genres ig JOIN genres g ON g.id = ig.genre_id
        WHERE ig.item_id = i.id AND g.name IN (`+strings.Join(genrePlaceholders, ",")+`))`+
		ratingPredicate+`
ORDER BY i.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list channel movies: %w", err)
	}
	return scanChannelItems(rows)
}

// ChannelShorts lists every airable film in the shorts library.
func (s *Store) ChannelShorts(ctx context.Context) ([]ChannelItem, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT i.id, m.duration_ms
FROM items i
JOIN media_files m ON m.item_id = i.id
JOIN libraries l ON l.id = i.library_id
WHERE `+channelEligibility+` AND i.kind = 'movie' AND l.kind = 'shorts'
ORDER BY i.id`)
	if err != nil {
		return nil, fmt.Errorf("list channel shorts: %w", err)
	}
	return scanChannelItems(rows)
}

func scanChannelItems(rows *sql.Rows) ([]ChannelItem, error) {
	defer func() { _ = rows.Close() }()
	var result []ChannelItem
	for rows.Next() {
		var item ChannelItem
		if err := rows.Scan(&item.ItemID, &item.DurationMS); err != nil {
			return nil, fmt.Errorf("scan channel item: %w", err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// ChannelCursors returns where each of a channel's sources left off.
func (s *Store) ChannelCursors(ctx context.Context, channelID int64) ([]ChannelCursor, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT source, cycle, cycle_started_at, item_id FROM channel_cursors WHERE channel_id = ? ORDER BY source`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list channel %d cursors: %w", channelID, err)
	}
	defer func() { _ = rows.Close() }()
	var result []ChannelCursor
	for rows.Next() {
		var cursor ChannelCursor
		if err := rows.Scan(&cursor.Source, &cursor.Cycle, &cursor.CycleStartedAt, &cursor.ItemID); err != nil {
			return nil, fmt.Errorf("scan channel cursor: %w", err)
		}
		result = append(result, cursor)
	}
	return result, rows.Err()
}

// ChannelAired lists everything a channel has stored, oldest first: its
// retained history and its scheduled tail alike, since to a source planning
// ahead a program already laid down has aired.
func (s *Store) ChannelAired(ctx context.Context, channelID int64) ([]AiredProgram, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT item_id, starts_at FROM channel_programs WHERE channel_id = ? ORDER BY starts_at, id`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list channel %d history: %w", channelID, err)
	}
	defer func() { _ = rows.Close() }()
	var result []AiredProgram
	for rows.Next() {
		var program AiredProgram
		if err := rows.Scan(&program.ItemID, &program.StartsAt); err != nil {
			return nil, fmt.Errorf("scan channel history: %w", err)
		}
		result = append(result, program)
	}
	return result, rows.Err()
}

// ChannelLastProgram reports the item and end of the latest program a channel
// has stored, or zero and an empty string when it has none.
func (s *Store) ChannelLastProgram(ctx context.Context, channelID int64) (int64, string, error) {
	var itemID int64
	var end string
	err := s.db.QueryRowContext(ctx, `
SELECT item_id, ends_at FROM channel_programs WHERE channel_id = ?
ORDER BY ends_at DESC, id DESC LIMIT 1`, channelID).Scan(&itemID, &end)
	if err == sql.ErrNoRows {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("read channel %d last program: %w", channelID, err)
	}
	return itemID, end, nil
}

// ChannelScheduleReach reports how far the whole lineup is scheduled: the
// earliest end among the channels that carry programs, empty when none do.
// Channels with nothing to air are ignored, otherwise one of them would keep
// the lineup permanently short of its horizon.
func (s *Store) ChannelScheduleReach(ctx context.Context) (string, error) {
	var reach string
	err := s.db.QueryRowContext(ctx, `
SELECT COALESCE(MIN(last_end), '')
FROM (SELECT MAX(ends_at) AS last_end FROM channel_programs GROUP BY channel_id)`).Scan(&reach)
	if err != nil {
		return "", fmt.Errorf("read channel schedule reach: %w", err)
	}
	return reach, nil
}

// AppendChannelPrograms writes a channel's new tail and the cursors that
// produced it in one transaction, so a crash between the two cannot make a
// source replay what it already scheduled.
func (s *Store) AppendChannelPrograms(
	ctx context.Context, channelID int64, programs []ScheduledProgram, cursors []ChannelCursor,
) error {
	if len(programs) == 0 && len(cursors) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("append channel programs transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, program := range programs {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO channel_programs(channel_id, item_id, starts_at, ends_at) VALUES (?, ?, ?, ?)`,
			channelID, program.ItemID, program.StartsAt, program.EndsAt); err != nil {
			return fmt.Errorf("append channel %d program: %w", channelID, err)
		}
	}
	for _, cursor := range cursors {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO channel_cursors(channel_id, source, cycle, cycle_started_at, item_id) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(channel_id, source) DO UPDATE SET cycle = excluded.cycle,
    cycle_started_at = excluded.cycle_started_at, item_id = excluded.item_id`,
			channelID, cursor.Source, cursor.Cycle, cursor.CycleStartedAt, cursor.ItemID); err != nil {
			return fmt.Errorf("save channel %d cursor %q: %w", channelID, cursor.Source, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit channel %d programs: %w", channelID, err)
	}
	return nil
}

// PruneChannelPrograms drops programs that ended before the given instant.
func (s *Store) PruneChannelPrograms(ctx context.Context, before string) (int, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM channel_programs WHERE ends_at < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("prune channel programs: %w", err)
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read pruned channel program count: %w", err)
	}
	return int(removed), nil
}

// ChannelLineup returns every channel by number together with the programs
// overlapping the window, each channel's ascending by start. An item that went
// unavailable keeps its programs: the client shows what it cannot play rather
// than a hole in the schedule.
func (s *Store) ChannelLineup(ctx context.Context, from, to string) ([]Channel, map[int64][]ChannelProgram, error) {
	channels, err := s.Channels(ctx)
	if err != nil {
		return nil, nil, err
	}
	// A channel hands episodes to the client outside their show hierarchy,
	// so each carries its series title like search and Next Up do.
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemColumns+`, COALESCE(series.title, ''),
    p.id, p.channel_id, p.starts_at, p.ends_at, COALESCE(m.id, 0), COALESCE(m.path, '')
FROM channel_programs p
JOIN items i ON i.id = p.item_id`+seriesJoin+`
LEFT JOIN media_files m ON m.item_id = i.id
WHERE p.ends_at > ? AND p.starts_at < ?
ORDER BY p.channel_id, p.starts_at, p.id`, from, to)
	if err != nil {
		return nil, nil, fmt.Errorf("list channel programs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var programs []ChannelProgram
	for rows.Next() {
		var program ChannelProgram
		var seriesTitle string
		item, err := scanItemFields(rows, &seriesTitle, &program.ID, &program.ChannelID, &program.StartsAt,
			&program.EndsAt, &program.MediaID, &program.MediaPath)
		if err != nil {
			return nil, nil, err
		}
		item.SeriesTitle = seriesTitle
		program.Item = item
		programs = append(programs, program)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	// A channel airs the same item repeatedly, and genres are loaded per item
	// id, so the duplicates are collapsed and the result copied back.
	unique := make([]Item, 0, len(programs))
	positions := make(map[int64]int, len(programs))
	for _, program := range programs {
		if _, ok := positions[program.Item.ID]; ok {
			continue
		}
		positions[program.Item.ID] = len(unique)
		unique = append(unique, program.Item)
	}
	if err := s.populateGenres(ctx, unique); err != nil {
		return nil, nil, err
	}
	videos, err := s.firstVideoStreams(ctx, programs)
	if err != nil {
		return nil, nil, err
	}
	byChannel := make(map[int64][]ChannelProgram, len(channels))
	for index := range programs {
		programs[index].Item = unique[positions[programs[index].Item.ID]]
		if video, ok := videos[programs[index].MediaID]; ok {
			stream := video
			programs[index].Video = &stream
		}
		byChannel[programs[index].ChannelID] = append(byChannel[programs[index].ChannelID], programs[index])
	}
	return channels, byChannel, nil
}

// firstVideoStreams returns the first video stream of every file behind the
// given programs, keyed by media file id.
func (s *Store) firstVideoStreams(ctx context.Context, programs []ChannelProgram) (map[int64]Stream, error) {
	seen := make(map[int64]bool, len(programs))
	var args []any
	var placeholders []string
	for _, program := range programs {
		if program.MediaID == 0 || seen[program.MediaID] {
			continue
		}
		seen[program.MediaID] = true
		args = append(args, program.MediaID)
		placeholders = append(placeholders, "?")
	}
	result := make(map[int64]Stream, len(args))
	if len(args) == 0 {
		return result, nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT media_file_id, stream_index, kind, codec, width, height, dynamic_range
FROM media_streams
WHERE kind = 'video' AND media_file_id IN (`+strings.Join(placeholders, ",")+`)
ORDER BY media_file_id, stream_index`, args...)
	if err != nil {
		return nil, fmt.Errorf("list channel program video streams: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var mediaID int64
		var stream Stream
		if err := rows.Scan(&mediaID, &stream.Index, &stream.Kind, &stream.Codec,
			&stream.Width, &stream.Height, &stream.DynamicRange); err != nil {
			return nil, fmt.Errorf("scan channel program video stream: %w", err)
		}
		if _, ok := result[mediaID]; ok {
			continue
		}
		stream.Resolution = videoResolution(stream.Width, stream.Height)
		result[mediaID] = stream
	}
	return result, rows.Err()
}

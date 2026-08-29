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

// Channel is one generated linear channel. A number is assigned when a key
// first appears and is never reused, so a channel a viewer has learned keeps
// its place even as the lineup grows.
type Channel struct {
	ID     int64  `json:"id"`
	Number int    `json:"number"`
	Key    string `json:"key"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	// ItemID names the show behind a show channel and GenreID the genre behind
	// a genre channel. Both are zero for the hdr and mix channels.
	ItemID  int64 `json:"-"`
	GenreID int64 `json:"-"`
}

// ChannelCandidate is a show or genre that qualifies for a channel, with the
// count that ranked it.
type ChannelCandidate struct {
	ID    int64
	Name  string
	Count int
}

// ChannelItem is a schedulable item: what laying out a program needs and
// nothing more.
type ChannelItem struct {
	ItemID     int64
	DurationMS int64
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

// channelEligibility is what every channel can schedule: an available movie or
// episode with a file long enough to occupy a slot.
const channelEligibility = `i.available = 1 AND i.kind IN ('movie', 'episode') AND m.duration_ms > 0`

// firstVideoDynamicRange is the file's first video stream classification, which
// is what decides whether an item belongs on the HDR channel.
const firstVideoDynamicRange = `(SELECT stream.dynamic_range FROM media_streams stream
        WHERE stream.media_file_id = m.id AND stream.kind = 'video'
        ORDER BY stream.stream_index LIMIT 1)`

// TopChannelShows ranks shows by how many episodes they can actually air.
// Specials are excluded because a show channel skips them.
func (s *Store) TopChannelShows(ctx context.Context, limit int) ([]ChannelCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT show.id, show.title, COUNT(*)
FROM items i
JOIN media_files m ON m.item_id = i.id
JOIN items season ON season.id = i.parent_id
JOIN items show ON show.id = season.parent_id
WHERE `+channelEligibility+` AND i.kind = 'episode' AND i.season_number > 0 AND show.available = 1
GROUP BY show.id
ORDER BY COUNT(*) DESC, show.title COLLATE NOCASE, show.id
LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("rank channel shows: %w", err)
	}
	return scanChannelCandidates(rows, "show")
}

// TopChannelMovieGenres ranks genres by how many playable movies carry them.
func (s *Store) TopChannelMovieGenres(ctx context.Context, limit int) ([]ChannelCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT g.id, g.name, COUNT(*)
FROM items i
JOIN media_files m ON m.item_id = i.id
JOIN item_genres ig ON ig.item_id = i.id
JOIN genres g ON g.id = ig.genre_id
WHERE `+channelEligibility+` AND i.kind = 'movie'
GROUP BY g.id
ORDER BY COUNT(*) DESC, g.name COLLATE NOCASE, g.id
LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("rank channel genres: %w", err)
	}
	return scanChannelCandidates(rows, "genre")
}

func scanChannelCandidates(rows *sql.Rows, kind string) ([]ChannelCandidate, error) {
	defer func() { _ = rows.Close() }()
	var result []ChannelCandidate
	for rows.Next() {
		var candidate ChannelCandidate
		if err := rows.Scan(&candidate.ID, &candidate.Name, &candidate.Count); err != nil {
			return nil, fmt.Errorf("scan channel %s candidate: %w", kind, err)
		}
		result = append(result, candidate)
	}
	return result, rows.Err()
}

func (s *Store) Channels(ctx context.Context) ([]Channel, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, number, key, name, kind, COALESCE(item_id, 0), COALESCE(genre_id, 0)
FROM channels ORDER BY number`)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]Channel, 0)
	for rows.Next() {
		var channel Channel
		if err := rows.Scan(&channel.ID, &channel.Number, &channel.Key, &channel.Name,
			&channel.Kind, &channel.ItemID, &channel.GenreID); err != nil {
			return nil, fmt.Errorf("scan channel: %w", err)
		}
		result = append(result, channel)
	}
	return result, rows.Err()
}

// CreateChannel adds a channel at the next unused number. Numbers are never
// reused, so a channel that stops qualifying does not hand its number to a
// later one.
func (s *Store) CreateChannel(ctx context.Context, channel Channel, createdAt string) (Channel, error) {
	err := s.db.QueryRowContext(ctx, `
INSERT INTO channels(number, key, name, kind, item_id, genre_id, created_at)
VALUES ((SELECT COALESCE(MAX(number), 0) + 1 FROM channels), ?, ?, ?, ?, ?, ?)
RETURNING id, number`, channel.Key, channel.Name, channel.Kind,
		nullableID(channel.ItemID), nullableID(channel.GenreID), createdAt).
		Scan(&channel.ID, &channel.Number)
	if err != nil {
		return Channel{}, fmt.Errorf("create channel %q: %w", channel.Key, err)
	}
	return channel, nil
}

func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// RenameChannel follows the catalog when a show or genre is renamed.
func (s *Store) RenameChannel(ctx context.Context, id int64, name string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE channels SET name = ? WHERE id = ?`, name, id); err != nil {
		return fmt.Errorf("rename channel %d: %w", id, err)
	}
	return nil
}

// ChannelEligibleItems returns what a channel can schedule, in the order the
// generator walks it: season and episode order for a show channel, id order
// elsewhere so a seeded pick is reproducible.
func (s *Store) ChannelEligibleItems(ctx context.Context, channel Channel) ([]ChannelItem, error) {
	predicate := ""
	order := "i.id"
	var args []any
	switch channel.Kind {
	case "show":
		predicate = ` AND i.kind = 'episode' AND i.season_number > 0
    AND i.parent_id IN (SELECT id FROM items WHERE parent_id = ? AND kind = 'season')`
		order = "i.season_number, i.episode_number, i.id"
		args = append(args, channel.ItemID)
	case "genre":
		predicate = ` AND EXISTS (SELECT 1 FROM item_genres ig
        WHERE ig.item_id = i.id AND ig.genre_id = ?)`
		args = append(args, channel.GenreID)
	case "hdr":
		predicate = ` AND ` + firstVideoDynamicRange + ` IN ('hdr', 'dolby_vision')`
	case "mix":
	default:
		return nil, fmt.Errorf("unknown channel kind %q", channel.Kind)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT i.id, m.duration_ms
FROM items i JOIN media_files m ON m.item_id = i.id
WHERE `+channelEligibility+predicate+`
ORDER BY `+order, args...)
	if err != nil {
		return nil, fmt.Errorf("list channel %q items: %w", channel.Key, err)
	}
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

// ChannelProgramItems lists what a channel currently has scheduled, oldest
// first, which is both the set a shuffled channel avoids repeating and the
// trail a show channel continues from.
func (s *Store) ChannelProgramItems(ctx context.Context, channelID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT item_id FROM channel_programs WHERE channel_id = ? ORDER BY starts_at, id`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list channel %d programs: %w", channelID, err)
	}
	defer func() { _ = rows.Close() }()
	var result []int64
	for rows.Next() {
		var itemID int64
		if err := rows.Scan(&itemID); err != nil {
			return nil, fmt.Errorf("scan channel program item: %w", err)
		}
		result = append(result, itemID)
	}
	return result, rows.Err()
}

// ChannelScheduleEnd reports when a channel's schedule runs out, or an empty
// string when it has no programs.
func (s *Store) ChannelScheduleEnd(ctx context.Context, channelID int64) (string, error) {
	var end sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT MAX(ends_at) FROM channel_programs WHERE channel_id = ?`, channelID).Scan(&end); err != nil {
		return "", fmt.Errorf("read channel %d schedule end: %w", channelID, err)
	}
	return end.String, nil
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

// AppendChannelPrograms writes a channel's new tail in one transaction.
func (s *Store) AppendChannelPrograms(ctx context.Context, channelID int64, programs []ScheduledProgram) error {
	if len(programs) == 0 {
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
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemColumns+`,
    p.id, p.channel_id, p.starts_at, p.ends_at, COALESCE(m.id, 0), COALESCE(m.path, '')
FROM channel_programs p
JOIN items i ON i.id = p.item_id
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
		item, err := scanItemFields(rows, &program.ID, &program.ChannelID, &program.StartsAt,
			&program.EndsAt, &program.MediaID, &program.MediaPath)
		if err != nil {
			return nil, nil, err
		}
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

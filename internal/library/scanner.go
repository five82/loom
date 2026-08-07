package library

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/five82/loom/internal/store"
)

// Scanner discovers read-only library contents and updates the durable catalog.
type MetadataMatcher interface {
	AutoMatch(context.Context, int64) error
}

type Scanner struct {
	store    *store.Store
	prober   Prober
	metadata MetadataMatcher
	logger   *slog.Logger
	movies   string
	shorts   string
	tv       string
}

func NewScanner(catalog *store.Store, prober Prober, metadata MetadataMatcher, movies, shorts, tv string, logger *slog.Logger) *Scanner {
	return &Scanner{store: catalog, prober: prober, metadata: metadata, movies: movies, shorts: shorts, tv: tv, logger: logger}
}

// Scan scans one library or all libraries when kind is empty.
func (s *Scanner) Scan(ctx context.Context, kind string) error {
	switch kind {
	case "movies":
		return s.scanLibrary(ctx, "movies", s.movies)
	case "shorts":
		return s.scanLibrary(ctx, "shorts", s.shorts)
	case "tv":
		return s.scanLibrary(ctx, "tv", s.tv)
	case "":
		movieErr := s.scanLibrary(ctx, "movies", s.movies)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		shortErr := s.scanLibrary(ctx, "shorts", s.shorts)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		tvErr := s.scanLibrary(ctx, "tv", s.tv)
		return errors.Join(movieErr, shortErr, tvErr)
	default:
		return fmt.Errorf("unknown library %q", kind)
	}
}

type scanCounters struct {
	discovered     int
	changed        int
	probeErrors    int
	metadataFailed bool
}

func (s *Scanner) scanLibrary(ctx context.Context, kind, root string) (resultErr error) {
	libraryID, scanID, err := s.store.StartScan(ctx, kind, root)
	if err != nil {
		return err
	}
	counters := &scanCounters{}
	defer func() {
		finishCtx := context.WithoutCancel(ctx)
		if err := s.store.FinishScan(finishCtx, libraryID, scanID, counters.discovered,
			counters.changed, counters.probeErrors, resultErr); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()

	s.logger.Info("library scan started", "library", kind, "path", root, "scan_id", scanID)
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read %s library root %q: %w", kind, root, err)
	}

	if kind == "movies" || kind == "shorts" {
		err = s.scanMovies(ctx, libraryID, scanID, root, entries, counters)
	} else {
		err = s.scanTV(ctx, libraryID, scanID, root, entries, counters)
	}
	if err != nil {
		return err
	}

	previous, err := s.store.AvailableMediaCount(ctx, libraryID)
	if err != nil {
		return err
	}
	if counters.discovered == 0 && previous > 0 {
		return fmt.Errorf("%s library scan found no media while %d files remain cataloged; refusing to reconcile a possibly unavailable mount", kind, previous)
	}
	s.logger.Info("library scan completed", "library", kind, "scan_id", scanID,
		"discovered_files", counters.discovered, "changed_files", counters.changed,
		"probe_errors", counters.probeErrors)
	return nil
}

func (s *Scanner) scanMovies(ctx context.Context, libraryID, scanID int64, root string, entries []os.DirEntry, counters *scanCounters) error {
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		children, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("read movie directory %q: %w", dir, err)
		}
		var candidates []os.DirEntry
		for _, child := range children {
			// Only direct children are considered. This deliberately ignores all
			// nested extras and behind-the-scenes media.
			if child.Type().IsRegular() && isVideo(child.Name()) {
				candidates = append(candidates, child)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		if len(candidates) > 1 {
			s.logger.Warn("movie directory skipped", "path", dir,
				"reason", "expected exactly one direct video file", "video_files", len(candidates))
			continue
		}
		title, year := parseNamedYear(entry.Name())
		itemID, err := s.store.UpsertItem(ctx, store.ItemInput{
			LibraryID: libraryID, SourceKey: entry.Name(), Kind: "movie",
			Title: title, Year: year, ScanID: scanID,
		})
		if err != nil {
			return err
		}
		path := filepath.Join(dir, candidates[0].Name())
		if err := s.scanMedia(ctx, itemID, scanID, path, counters); err != nil {
			return err
		}
		s.autoMatch(ctx, itemID, counters)
	}
	return nil
}

func (s *Scanner) scanTV(ctx context.Context, libraryID, scanID int64, root string, entries []os.DirEntry, counters *scanCounters) error {
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		showDir := filepath.Join(root, entry.Name())
		var videos []string
		err := filepath.WalkDir(showDir, func(path string, item fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if item.IsDir() {
				switch strings.ToLower(item.Name()) {
				case ".actors", "extrafanart", "metadata":
					if path != showDir {
						return filepath.SkipDir
					}
				}
				return nil
			}
			if item.Type().IsRegular() && isVideo(path) {
				videos = append(videos, path)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("walk TV directory %q: %w", showDir, err)
		}
		if len(videos) == 0 {
			continue
		}
		sort.Strings(videos)
		showTitle, showYear := parseNamedYear(entry.Name())
		showID, err := s.store.UpsertItem(ctx, store.ItemInput{
			LibraryID: libraryID, SourceKey: "show:" + entry.Name(), Kind: "show",
			Title: showTitle, Year: showYear, ScanID: scanID,
		})
		if err != nil {
			return err
		}
		seasonIDs := make(map[int]int64)
		episodePaths := make(map[string]string)
		for _, path := range videos {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return fmt.Errorf("resolve TV path %q: %w", path, err)
			}
			numbers, matched := parseEpisodeFilename(path)
			if !matched {
				parentID := showID
				title := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
				itemID, err := s.store.UpsertItem(ctx, store.ItemInput{
					LibraryID: libraryID, ParentID: &parentID, SourceKey: "file:" + relative,
					Kind: "unmatched", Title: title, ScanID: scanID,
				})
				if err != nil {
					return err
				}
				if err := s.scanMedia(ctx, itemID, scanID, path, counters); err != nil {
					return err
				}
				continue
			}
			seasonID, ok := seasonIDs[numbers.Season]
			if !ok {
				parentID := showID
				seasonID, err = s.store.UpsertItem(ctx, store.ItemInput{
					LibraryID: libraryID, ParentID: &parentID,
					SourceKey: fmt.Sprintf("show:%s:season:%d", entry.Name(), numbers.Season),
					Kind:      "season", Title: seasonTitle(numbers.Season),
					SeasonNumber: numbers.Season, ScanID: scanID,
				})
				if err != nil {
					return err
				}
				seasonIDs[numbers.Season] = seasonID
			}
			// An episode is identified by its number rather than its filename so a
			// replacement encode under a new name keeps the same item, and with it
			// the TMDB match and playback state.
			sourceKey := episodeSourceKey(entry.Name(), numbers)
			if existing, duplicate := episodePaths[sourceKey]; duplicate {
				// Two files claim one episode, which normally means a replacement
				// encode landed before the old file was removed. Keep the first in
				// sorted order so the choice does not flip between scans.
				s.logger.Warn("episode file skipped", "path", path,
					"reason", "another file already provides this episode", "using", existing)
				continue
			}
			episodePaths[sourceKey] = path
			parentID := seasonID
			itemID, err := s.store.UpsertItem(ctx, store.ItemInput{
				LibraryID: libraryID, ParentID: &parentID, SourceKey: sourceKey,
				Kind: "episode", Title: episodeTitle(numbers), SeasonNumber: numbers.Season,
				EpisodeNumber: numbers.Start, EpisodeEndNumber: numbers.End, ScanID: scanID,
			})
			if err != nil {
				return err
			}
			if err := s.scanMedia(ctx, itemID, scanID, path, counters); err != nil {
				return err
			}
		}
		s.autoMatch(ctx, showID, counters)
	}
	return nil
}

func (s *Scanner) autoMatch(ctx context.Context, itemID int64, counters *scanCounters) {
	if s.metadata == nil || counters.metadataFailed {
		return
	}
	if err := s.metadata.AutoMatch(ctx, itemID); err != nil && ctx.Err() == nil {
		// A provider outage should cost one failed request, not one timeout per
		// library item. The next manual or scheduled scan tries again.
		counters.metadataFailed = true
		s.logger.Warn("automatic metadata matching paused for this library scan", "item_id", itemID, "error", err)
	}
}

// episodeSourceKey extends the season key so an episode's catalog identity is
// its position in the show rather than the file that currently provides it.
func episodeSourceKey(showDir string, numbers episodeNumbers) string {
	return fmt.Sprintf("show:%s:season:%d:episode:%d-%d", showDir, numbers.Season, numbers.Start, numbers.End)
}

func seasonTitle(number int) string {
	if number == 0 {
		return "Specials"
	}
	return fmt.Sprintf("Season %d", number)
}

func (s *Scanner) scanMedia(ctx context.Context, itemID, scanID int64, path string, counters *scanCounters) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat media %q: %w", path, err)
	}
	counters.discovered++
	existing, err := s.store.MediaByPath(ctx, path)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	// The lookup is by path but the catalog stores media by item, so an unchanged
	// file still has to be re-recorded when it now belongs to a different item.
	// That happens when duplicate files for one episode are resolved and the
	// surviving item inherits the remaining file.
	if err == nil && existing.ItemID == itemID && existing.Size == info.Size() &&
		existing.MTimeNS == info.ModTime().UnixNano() && existing.ProbeError == "" {
		return s.store.TouchMedia(ctx, itemID, scanID)
	}

	counters.changed++
	probe, probeErr := s.prober.Probe(ctx, path)
	media := store.MediaFile{
		ItemID: itemID, Path: path, Size: info.Size(), MTimeNS: info.ModTime().UnixNano(),
		DurationMS: probe.DurationMS, Container: probe.Container, LastSeenScanID: scanID,
	}
	if probeErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		counters.probeErrors++
		media.ProbeError = probeErr.Error()
		s.logger.Warn("media probe failed", "path", path, "error", probeErr)
	}
	if _, err := s.store.UpsertMedia(ctx, media, probe.Streams); err != nil {
		return err
	}
	return nil
}

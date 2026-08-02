package metadata

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/five82/loom/internal/store"
	"github.com/five82/loom/internal/tmdb"
)

// Service owns metadata matching and provider image persistence.
type Service struct {
	store    *store.Store
	tmdb     *tmdb.Client
	imageDir string
	http     *http.Client
	logger   *slog.Logger
}

func New(catalog *store.Store, client *tmdb.Client, imageDir string, logger *slog.Logger) *Service {
	return &Service{
		store: catalog, tmdb: client, imageDir: imageDir,
		http: &http.Client{Timeout: 30 * time.Second}, logger: logger,
	}
}

func (s *Service) Search(ctx context.Context, mediaType, query string, year int) ([]tmdb.SearchResult, error) {
	return s.tmdb.Search(ctx, mediaType, query, year)
}

// AutoMatch applies a result only when title and year make the choice unambiguous.
func (s *Service) AutoMatch(ctx context.Context, itemID int64) error {
	item, err := s.store.Item(ctx, itemID)
	if err != nil {
		return err
	}
	if item.TMDBID != 0 || (item.Kind != "movie" && item.Kind != "show") {
		return nil
	}
	mediaType := metadataType(item.Kind)
	results, err := s.tmdb.Search(ctx, mediaType, item.Title, item.Year)
	if err != nil {
		return err
	}
	wantedTitle := normalizedTitle(item.Title)
	var matches []tmdb.SearchResult
	for _, result := range results {
		if normalizedTitle(result.Title) != wantedTitle && normalizedTitle(result.OriginalTitle) != wantedTitle {
			continue
		}
		if item.Year > 0 && result.Year != item.Year {
			continue
		}
		matches = append(matches, result)
	}
	if len(matches) != 1 {
		return nil
	}
	s.logger.Info("metadata automatically matched", "item_id", item.ID,
		"title", item.Title, "tmdb_id", matches[0].ID)
	return s.Match(ctx, itemID, matches[0].ID)
}

func (s *Service) Match(ctx context.Context, itemID, tmdbID int64) error {
	item, err := s.store.Item(ctx, itemID)
	if err != nil {
		return err
	}
	if item.Kind != "movie" && item.Kind != "show" {
		return fmt.Errorf("item %d is %s; only movies and shows can be matched", itemID, item.Kind)
	}
	mediaType := metadataType(item.Kind)
	details, err := s.tmdb.Details(ctx, mediaType, tmdbID)
	if err != nil {
		return err
	}
	if err := s.store.UpdateMetadata(ctx, itemID, store.MetadataUpdate{
		TMDBID: details.ID, Title: details.Title, Year: details.Year,
		Overview: details.Overview, ReleaseDate: details.ReleaseDate,
	}); err != nil {
		return err
	}
	s.saveProviderImage(ctx, itemID, "poster", details.PosterPath)
	s.saveProviderImage(ctx, itemID, "backdrop", details.BackdropPath)
	if item.Kind == "show" {
		s.updateEpisodes(ctx, itemID, tmdbID)
	}
	s.logger.Info("metadata match applied", "item_id", itemID, "tmdb_id", tmdbID, "type", mediaType)
	return nil
}

func (s *Service) updateEpisodes(ctx context.Context, showID, tmdbID int64) {
	localEpisodes, err := s.store.EpisodesForShow(ctx, showID)
	if err != nil {
		s.logger.Warn("TV episode metadata not updated", "show_id", showID, "error", err)
		return
	}
	bySeason := make(map[int][]store.Item)
	for _, episode := range localEpisodes {
		bySeason[episode.SeasonNumber] = append(bySeason[episode.SeasonNumber], episode)
	}
	for seasonNumber, items := range bySeason {
		episodes, err := s.tmdb.Season(ctx, tmdbID, seasonNumber)
		if err != nil {
			s.logger.Warn("TMDB season metadata unavailable", "show_id", showID,
				"season", seasonNumber, "error", err)
			continue
		}
		byNumber := make(map[int]tmdb.Episode, len(episodes))
		for _, episode := range episodes {
			byNumber[episode.Number] = episode
		}
		for _, item := range items {
			metadata, ok := combinedEpisodeMetadata(item, byNumber)
			if !ok {
				continue
			}
			if err := s.store.UpdateMetadata(ctx, item.ID, metadata); err != nil {
				s.logger.Warn("episode metadata not updated", "item_id", item.ID, "error", err)
			}
		}
	}
}

func combinedEpisodeMetadata(item store.Item, episodes map[int]tmdb.Episode) (store.MetadataUpdate, bool) {
	end := item.EpisodeEndNumber
	if end < item.EpisodeNumber {
		end = item.EpisodeNumber
	}
	var titles, overviews []string
	var first tmdb.Episode
	for number := item.EpisodeNumber; number <= end; number++ {
		episode, ok := episodes[number]
		if !ok {
			return store.MetadataUpdate{}, false
		}
		if first.ID == 0 {
			first = episode
		}
		titles = append(titles, episode.Title)
		if episode.Overview != "" {
			overviews = append(overviews, episode.Overview)
		}
	}
	return store.MetadataUpdate{
		TMDBID: first.ID, Title: strings.Join(titles, " / "), Overview: strings.Join(overviews, "\n\n"),
		ReleaseDate: first.ReleaseDate,
	}, true
}

func (s *Service) saveProviderImage(ctx context.Context, itemID int64, kind, providerPath string) {
	imageURL := s.tmdb.ImageURL(providerPath)
	if imageURL == "" {
		return
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		s.logger.Warn("metadata image not saved", "item_id", itemID, "kind", kind, "error", err)
		return
	}
	response, err := s.http.Do(request)
	if err != nil {
		s.logger.Warn("metadata image not saved", "item_id", itemID, "kind", kind, "error", err)
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		s.logger.Warn("metadata image not saved", "item_id", itemID, "kind", kind,
			"error", "image server returned "+response.Status)
		return
	}
	extension := strings.ToLower(filepath.Ext(providerPath))
	if extension != ".jpg" && extension != ".jpeg" && extension != ".png" && extension != ".webp" {
		extension = ".jpg"
	}
	dir := filepath.Join(s.imageDir, strconv.FormatInt(itemID, 10))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.logger.Warn("metadata image not saved", "item_id", itemID, "kind", kind, "error", err)
		return
	}
	path := filepath.Join(dir, kind+extension)
	temporary, err := os.CreateTemp(dir, ".image-*")
	if err != nil {
		s.logger.Warn("metadata image not saved", "item_id", itemID, "kind", kind, "error", err)
		return
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	_, copyErr := io.Copy(temporary, io.LimitReader(response.Body, 25<<20))
	closeErr := temporary.Close()
	if copyErr != nil || closeErr != nil {
		s.logger.Warn("metadata image not saved", "item_id", itemID, "kind", kind,
			"error", errors.Join(copyErr, closeErr))
		return
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		s.logger.Warn("metadata image not saved", "item_id", itemID, "kind", kind, "error", err)
		return
	}
	if _, err := s.store.UpsertImage(ctx, store.Image{
		ItemID: itemID, Kind: kind, Path: path, SourceURL: imageURL,
	}); err != nil {
		s.logger.Warn("metadata image not recorded", "item_id", itemID, "kind", kind, "error", err)
	}
}

func metadataType(kind string) string {
	if kind == "show" {
		return "tv"
	}
	return "movie"
}

func normalizedTitle(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, value)
}

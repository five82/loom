package metadata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/five82/loom/internal/store"
	"github.com/five82/loom/internal/tmdb"
)

const maxImageBytes = 25 << 20

var (
	ErrImageOptionNotFound = errors.New("image option not found")
	ErrImageUnavailable    = errors.New("image selection is unavailable")
)

// Service owns metadata matching and provider image persistence.
type Service struct {
	store    *store.Store
	tmdb     *tmdb.Client
	imageDir string
	http     *http.Client
	logger   *slog.Logger
	imageMu  sync.Mutex
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
	identityChanged := item.TMDBID != 0 && item.TMDBID != details.ID
	if identityChanged {
		// Never carry a manual selection across two different titles. Clearing
		// first also lets a later refresh retry if the new download fails.
		for _, kind := range []string{"poster", "backdrop", "logo"} {
			s.clearImage(ctx, itemID, kind)
		}
	}
	s.saveDefaultImage(ctx, itemID, "poster", details.PosterPath)
	s.saveDefaultImage(ctx, itemID, "backdrop", details.BackdropPath)
	s.saveDefaultLogo(ctx, itemID, mediaType, tmdbID)
	if item.Kind == "show" {
		s.updateEpisodes(ctx, itemID, tmdbID)
	}
	s.logger.Info("metadata match applied", "item_id", itemID, "tmdb_id", tmdbID, "type", mediaType)
	return nil
}

type ImageOption struct {
	Provider     string  `json:"provider"`
	ProviderPath string  `json:"provider_path"`
	Language     string  `json:"language,omitempty"`
	Width        int     `json:"width"`
	Height       int     `json:"height"`
	AspectRatio  float64 `json:"aspect_ratio,omitempty"`
	VoteAverage  float64 `json:"vote_average,omitempty"`
	VoteCount    int     `json:"vote_count,omitempty"`
	ThumbnailURL string  `json:"thumbnail_url"`
	Selected     bool    `json:"selected"`
}

func (s *Service) ImageOptions(ctx context.Context, itemID int64, kind string) ([]ImageOption, error) {
	item, err := s.imageItem(ctx, itemID, kind)
	if err != nil {
		return nil, err
	}
	images, err := s.tmdb.Images(ctx, metadataType(item.Kind), item.TMDBID)
	if err != nil {
		return nil, err
	}
	candidates, thumbnailSize := imageCandidates(images, kind)
	current, err := s.store.ItemImage(ctx, itemID, kind)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	options := make([]ImageOption, 0, len(candidates))
	for _, candidate := range candidates {
		options = append(options, ImageOption{
			Provider: "tmdb", ProviderPath: candidate.FilePath, Language: candidate.Language,
			Width: candidate.Width, Height: candidate.Height, AspectRatio: candidate.AspectRatio,
			VoteAverage: candidate.VoteAverage, VoteCount: candidate.VoteCount,
			ThumbnailURL: s.tmdb.ImageURLSize(candidate.FilePath, thumbnailSize),
			Selected:     current != nil && current.Provider == "tmdb" && current.ProviderPath == candidate.FilePath,
		})
	}
	return options, nil
}

func (s *Service) SelectImage(
	ctx context.Context, itemID int64, kind, provider, providerPath string,
) (*store.Image, error) {
	if provider != "tmdb" || providerPath == "" {
		return nil, ErrImageOptionNotFound
	}
	options, err := s.ImageOptions(ctx, itemID, kind)
	if err != nil {
		return nil, err
	}
	for _, option := range options {
		if option.Provider == provider && option.ProviderPath == providerPath {
			selected, err := s.downloadProviderImage(ctx, itemID, kind, providerPath, true, true)
			if err == nil {
				s.logger.Info("metadata image selected", "item_id", itemID, "kind", kind,
					"provider", provider, "provider_path", providerPath)
			}
			return selected, err
		}
	}
	return nil, ErrImageOptionNotFound
}

func (s *Service) ResetImage(ctx context.Context, itemID int64, kind string) (*store.Image, error) {
	item, err := s.imageItem(ctx, itemID, kind)
	if err != nil {
		return nil, err
	}
	providerPath := ""
	if kind == "logo" {
		images, err := s.tmdb.Images(ctx, metadataType(item.Kind), item.TMDBID)
		if err != nil {
			return nil, err
		}
		if len(images.Logos) > 0 {
			providerPath = images.Logos[0].FilePath
		}
	} else {
		details, err := s.tmdb.Details(ctx, metadataType(item.Kind), item.TMDBID)
		if err != nil {
			return nil, err
		}
		providerPath = details.PosterPath
		if kind == "backdrop" {
			providerPath = details.BackdropPath
		}
	}
	if providerPath == "" {
		return nil, fmt.Errorf("%w: TMDB has no default %s for item %d", ErrImageUnavailable, kind, itemID)
	}
	reset, err := s.downloadProviderImage(ctx, itemID, kind, providerPath, false, true)
	if err == nil {
		s.logger.Info("metadata image reset", "item_id", itemID, "kind", kind,
			"provider_path", providerPath)
	}
	return reset, err
}

func (s *Service) imageItem(ctx context.Context, itemID int64, kind string) (*store.Item, error) {
	if kind != "poster" && kind != "backdrop" && kind != "logo" {
		return nil, fmt.Errorf("%w: unsupported image kind %q", ErrImageUnavailable, kind)
	}
	item, err := s.store.Item(ctx, itemID)
	if err != nil {
		return nil, err
	}
	if item.Kind != "movie" && item.Kind != "show" {
		return nil, fmt.Errorf("%w: item %d is %s; images can only be selected for movies and shows", ErrImageUnavailable, itemID, item.Kind)
	}
	if item.TMDBID == 0 {
		return nil, fmt.Errorf("%w: item %d does not have a TMDB match", ErrImageUnavailable, itemID)
	}
	return item, nil
}

func (s *Service) saveDefaultImage(ctx context.Context, itemID int64, kind, providerPath string) {
	if providerPath == "" {
		return
	}
	if _, err := s.downloadProviderImage(ctx, itemID, kind, providerPath, false, false); err != nil {
		s.logger.Warn("metadata image not saved", "item_id", itemID, "kind", kind, "error", err)
	}
}

func (s *Service) saveDefaultLogo(ctx context.Context, itemID int64, mediaType string, tmdbID int64) {
	images, err := s.tmdb.Images(ctx, mediaType, tmdbID)
	if err != nil {
		s.logger.Warn("TMDB logo metadata unavailable", "item_id", itemID, "error", err)
		return
	}
	if len(images.Logos) > 0 {
		s.saveDefaultImage(ctx, itemID, "logo", images.Logos[0].FilePath)
	}
}

func imageCandidates(images tmdb.Images, kind string) ([]tmdb.ImageCandidate, string) {
	switch kind {
	case "backdrop":
		return images.Backdrops, "w780"
	case "logo":
		return images.Logos, "w300"
	default:
		return images.Posters, "w342"
	}
}

func (s *Service) clearImage(ctx context.Context, itemID int64, kind string) {
	s.imageMu.Lock()
	defer s.imageMu.Unlock()
	current, err := s.store.ItemImage(ctx, itemID, kind)
	if errors.Is(err, store.ErrNotFound) {
		return
	}
	if err != nil {
		s.logger.Warn("metadata image not cleared", "item_id", itemID, "kind", kind, "error", err)
		return
	}
	if err := s.store.DeleteItemImage(ctx, itemID, kind); err != nil {
		s.logger.Warn("metadata image not cleared", "item_id", itemID, "kind", kind, "error", err)
		return
	}
	if err := os.Remove(current.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.logger.Warn("old metadata image file not removed", "path", current.Path, "error", err)
	}
}

func (s *Service) downloadProviderImage(
	ctx context.Context, itemID int64, kind, providerPath string, manuallySelected, force bool,
) (*store.Image, error) {
	s.imageMu.Lock()
	defer s.imageMu.Unlock()

	current, err := s.store.ItemImage(ctx, itemID, kind)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if current != nil && current.ManuallySelected && !force {
		return current, nil
	}
	imageURL := s.tmdb.ImageURL(providerPath)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create image request: %w", err)
	}
	response, err := s.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download image: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image server returned %s", response.Status)
	}

	dir := filepath.Join(s.imageDir, strconv.FormatInt(itemID, 10))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create image directory: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".image-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary image: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	hasher := sha256.New()
	limited := &io.LimitedReader{R: response.Body, N: maxImageBytes + 1}
	written, copyErr := io.Copy(io.MultiWriter(temporary, hasher), limited)
	closeErr := temporary.Close()
	if copyErr != nil || closeErr != nil {
		return nil, fmt.Errorf("save temporary image: %w", errors.Join(copyErr, closeErr))
	}
	if written > maxImageBytes {
		return nil, fmt.Errorf("image exceeds %d-byte limit", maxImageBytes)
	}

	file, err := os.Open(temporaryPath)
	if err != nil {
		return nil, fmt.Errorf("open downloaded image: %w", err)
	}
	config, format, decodeErr := image.DecodeConfig(file)
	closeErr = file.Close()
	if decodeErr != nil || closeErr != nil {
		return nil, fmt.Errorf("validate downloaded image: %w", errors.Join(decodeErr, closeErr))
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > 20000 || config.Height > 20000 {
		return nil, fmt.Errorf("invalid image dimensions %dx%d", config.Width, config.Height)
	}
	extension, contentType := ".jpg", "image/jpeg"
	if format == "png" {
		extension, contentType = ".png", "image/png"
	} else if format != "jpeg" {
		return nil, fmt.Errorf("unsupported image format %q", format)
	}
	tag := hex.EncodeToString(hasher.Sum(nil))
	path := filepath.Join(dir, kind+"-"+tag[:16]+extension)
	if err := os.Rename(temporaryPath, path); err != nil {
		return nil, fmt.Errorf("install downloaded image: %w", err)
	}
	pathWasActive := current != nil && current.Path == path
	_, err = s.store.UpsertImage(ctx, store.Image{
		ItemID: itemID, Kind: kind, Path: path, SourceURL: imageURL, Provider: "tmdb",
		ProviderPath: providerPath, Tag: tag, ContentType: contentType,
		Width: config.Width, Height: config.Height, ManuallySelected: manuallySelected,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		if !pathWasActive {
			_ = os.Remove(path)
		}
		return nil, err
	}
	if current != nil && current.Path != path {
		_ = os.Remove(current.Path)
	}
	return s.store.ItemImage(ctx, itemID, kind)
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

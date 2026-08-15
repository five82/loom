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

	"github.com/five82/loom/internal/images"
	"github.com/five82/loom/internal/store"
	"github.com/five82/loom/internal/tmdb"
)

const (
	maxImageBytes                     = 25 << 20
	automaticMatchMinVotes            = 5
	automaticMatchMinVoteAverage      = 2.0
	automaticMatchVoteDominanceFactor = 10
)

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

// AutoMatch applies unambiguous matches and backfills missing artwork for existing matches.
func (s *Service) AutoMatch(ctx context.Context, itemID int64) error {
	item, err := s.store.Item(ctx, itemID)
	if err != nil {
		return err
	}
	if item.Kind != "movie" && item.Kind != "show" {
		return nil
	}
	mediaType := metadataType(item.Kind)
	if item.TMDBID != 0 {
		var details *tmdb.Details
		if !item.DetailsLoaded {
			// An item matched before a field existed carries none of it, and
			// nothing else revisits a match that already has its TMDB id.
			loaded, err := s.tmdb.Details(ctx, mediaType, item.TMDBID)
			if err != nil {
				return err
			}
			if err := s.applyDetails(ctx, item, loaded); err != nil {
				return err
			}
			details = &loaded
		}
		if err := s.backfillImages(ctx, item, details); err != nil {
			return err
		}
		if item.Kind == "show" {
			// An episode added after the show was matched keeps the placeholder
			// title the scanner gave it until this runs, because nothing else
			// revisits a show that already has a match.
			s.updateEpisodes(ctx, item.ID, item.TMDBID, true)
		}
		return nil
	}
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
	selected, reason := selectAutomaticMatch(matches)
	if selected == nil {
		candidateIDs := make([]int64, len(matches))
		candidateVotes := make([]int, len(matches))
		candidateAverages := make([]float64, len(matches))
		for i, candidate := range matches {
			candidateIDs[i] = candidate.ID
			candidateVotes[i] = candidate.VoteCount
			candidateAverages[i] = candidate.VoteAverage
		}
		s.logger.Info("metadata automatic match skipped", "item_id", item.ID,
			"title", item.Title, "reason", reason, "candidate_ids", candidateIDs,
			"candidate_vote_counts", candidateVotes, "candidate_vote_averages", candidateAverages)
		return nil
	}
	s.logger.Info("metadata automatically matched", "item_id", item.ID,
		"title", item.Title, "tmdb_id", selected.ID, "candidate_count", len(matches),
		"vote_count", selected.VoteCount, "vote_average", selected.VoteAverage)
	return s.Match(ctx, itemID, selected.ID)
}

func (s *Service) backfillImages(ctx context.Context, item *store.Item, details *tmdb.Details) error {
	mediaType := metadataType(item.Kind)
	if item.PosterImageID == 0 {
		if details == nil {
			loaded, err := s.tmdb.Details(ctx, mediaType, item.TMDBID)
			if err != nil {
				return err
			}
			details = &loaded
		}
		s.saveDefaultImage(ctx, item.ID, "poster", details.PosterPath)
	}
	if item.BackdropImageID == 0 || item.LogoImageID == 0 || item.ThumbImageID == 0 {
		provided, err := s.tmdb.Images(ctx, mediaType, item.TMDBID)
		if err != nil {
			return err
		}
		if item.BackdropImageID == 0 {
			s.saveDefaultImage(ctx, item.ID, "backdrop", defaultBackdropPath(provided.Backdrops))
		}
		if item.LogoImageID == 0 && len(provided.Logos) > 0 {
			s.saveDefaultImage(ctx, item.ID, "logo", provided.Logos[0].FilePath)
		}
		if item.ThumbImageID == 0 {
			s.saveDefaultImage(ctx, item.ID, "thumb", defaultThumbPath(provided.Backdrops))
		}
	}
	if item.Kind == "show" {
		return s.backfillSeasonPosters(ctx, item.ID, item.TMDBID)
	}
	return nil
}

func (s *Service) backfillSeasonPosters(ctx context.Context, showID, tmdbID int64) error {
	seasons, err := s.store.SeasonsForShow(ctx, showID)
	if err != nil {
		return err
	}
	for _, season := range seasons {
		if _, err := s.store.ItemImage(ctx, season.ID, "poster"); err == nil {
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		details, err := s.tmdb.Season(ctx, tmdbID, season.SeasonNumber)
		if err != nil {
			// A season this library holds but TMDB does not is a numbering
			// disagreement, not a provider outage, so it must not cost the rest
			// of the scan its metadata the way a returned error would.
			var httpErr *tmdb.HTTPError
			if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
				s.logger.Warn("TMDB has no such season", "show_id", showID,
					"tmdb_id", tmdbID, "season", season.SeasonNumber)
				continue
			}
			return err
		}
		s.saveDefaultImage(ctx, season.ID, "poster", details.PosterPath)
	}
	return nil
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
	if err := s.applyDetails(ctx, item, details); err != nil {
		return err
	}
	identityChanged := item.TMDBID != 0 && item.TMDBID != details.ID
	if identityChanged {
		// Never carry artwork across two different titles. Clearing first also
		// lets a later refresh retry if a new download fails.
		for _, kind := range []string{"poster", "backdrop", "logo", "thumb"} {
			s.clearImage(ctx, itemID, kind)
		}
		if item.Kind == "show" {
			s.clearSeasonPosters(ctx, itemID)
			s.clearEpisodeThumbs(ctx, itemID)
		}
	}
	s.saveDefaultImage(ctx, itemID, "poster", details.PosterPath)
	s.saveDefaultArtwork(ctx, itemID, mediaType, tmdbID, details.BackdropPath)
	if item.Kind == "show" {
		s.updateEpisodes(ctx, itemID, tmdbID, false)
	}
	s.logger.Info("metadata match applied", "item_id", itemID, "tmdb_id", tmdbID, "type", mediaType)
	return nil
}

// applyDetails writes one TMDB detail fetch to the catalog. Only movies browse
// by genre, so a show's genres are left unstored rather than filling a facet
// nothing reads. Directors are movie-only for a different reason: TV credits
// them per episode, and a show's own crew list rarely names anyone a viewer
// would call its director.
func (s *Service) applyDetails(ctx context.Context, item *store.Item, details tmdb.Details) error {
	update := store.MetadataUpdate{
		TMDBID: details.ID, Title: details.Title, Year: details.Year,
		Overview: details.Overview, Tagline: details.Tagline, ReleaseDate: details.ReleaseDate,
		VoteAverage: details.VoteAverage, ContentRating: details.ContentRating,
		Status: details.Status, TotalSeasons: details.TotalSeasons,
		Credits: storeCredits(details.Cast, nil, nil),
	}
	if item.Kind == "movie" {
		update.Genres = storeGenres(details.Genres)
		update.Credits = storeCredits(details.Cast, details.Directors, details.Producers)
	}
	return s.store.UpdateMetadata(ctx, item.ID, update)
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
	item, provider, err := s.imageItem(ctx, itemID, kind)
	if err != nil {
		return nil, err
	}
	var candidates []tmdb.ImageCandidate
	thumbnailSize := "w342"
	if item.Kind == "season" {
		candidates, err = s.tmdb.SeasonImages(ctx, provider.TMDBID, item.SeasonNumber)
		if isTMDBNotFound(err) {
			err = nil
		}
	} else {
		var images tmdb.Images
		images, err = s.tmdb.Images(ctx, metadataType(item.Kind), item.TMDBID)
		if err == nil {
			candidates, thumbnailSize = imageCandidates(images, kind)
		}
	}
	if err != nil {
		return nil, err
	}
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
	// The path is trusted without refetching the TMDB options list: it came
	// from ImageOptions moments ago, revalidating costs a full TMDB round trip
	// per tap, and a bogus path just fails the download below.
	if _, _, err := s.imageItem(ctx, itemID, kind); err != nil {
		return nil, err
	}
	selected, err := s.downloadProviderImage(ctx, itemID, kind, providerPath, true, true)
	if err == nil {
		s.logger.Info("metadata image selected", "item_id", itemID, "kind", kind,
			"provider", provider, "provider_path", providerPath)
	}
	return selected, err
}

func (s *Service) ResetImage(ctx context.Context, itemID int64, kind string) (*store.Image, error) {
	item, provider, err := s.imageItem(ctx, itemID, kind)
	if err != nil {
		return nil, err
	}
	providerPath := ""
	if item.Kind == "season" {
		details, err := s.tmdb.Season(ctx, provider.TMDBID, item.SeasonNumber)
		if isTMDBNotFound(err) {
			return nil, fmt.Errorf(
				"%w: TMDB has no season %d for item %d",
				ErrImageUnavailable, item.SeasonNumber, itemID,
			)
		}
		if err != nil {
			return nil, err
		}
		providerPath = details.PosterPath
	} else if kind == "poster" {
		details, err := s.tmdb.Details(ctx, metadataType(item.Kind), item.TMDBID)
		if err != nil {
			return nil, err
		}
		providerPath = details.PosterPath
	} else {
		provided, err := s.tmdb.Images(ctx, metadataType(item.Kind), item.TMDBID)
		if err != nil {
			return nil, err
		}
		switch kind {
		case "backdrop":
			providerPath = defaultBackdropPath(provided.Backdrops)
		case "thumb":
			providerPath = defaultThumbPath(provided.Backdrops)
		case "logo":
			if len(provided.Logos) > 0 {
				providerPath = provided.Logos[0].FilePath
			}
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

func isTMDBNotFound(err error) bool {
	var responseError *tmdb.HTTPError
	return errors.As(err, &responseError) && responseError.StatusCode == http.StatusNotFound
}

func (s *Service) imageItem(ctx context.Context, itemID int64, kind string) (*store.Item, *store.Item, error) {
	if kind != "poster" && kind != "backdrop" && kind != "logo" && kind != "thumb" {
		return nil, nil, fmt.Errorf("%w: unsupported image kind %q", ErrImageUnavailable, kind)
	}
	item, err := s.store.Item(ctx, itemID)
	if err != nil {
		return nil, nil, err
	}
	provider := item
	if item.Kind == "season" {
		if kind != "poster" {
			return nil, nil, fmt.Errorf("%w: seasons only support poster selection", ErrImageUnavailable)
		}
		if item.ParentID == nil {
			return nil, nil, fmt.Errorf("%w: season %d does not have a parent show", ErrImageUnavailable, itemID)
		}
		provider, err = s.store.Item(ctx, *item.ParentID)
		if err != nil {
			return nil, nil, err
		}
	} else if item.Kind != "movie" && item.Kind != "show" {
		return nil, nil, fmt.Errorf("%w: item %d is %s; images cannot be selected", ErrImageUnavailable, itemID, item.Kind)
	}
	if provider.TMDBID == 0 {
		return nil, nil, fmt.Errorf("%w: item %d does not have a TMDB match", ErrImageUnavailable, itemID)
	}
	return item, provider, nil
}

func (s *Service) saveDefaultImage(ctx context.Context, itemID int64, kind, providerPath string) {
	s.saveDefaultImageSize(ctx, itemID, kind, providerPath, "original")
}

func (s *Service) saveDefaultImageSize(
	ctx context.Context, itemID int64, kind, providerPath, size string,
) {
	if providerPath == "" {
		return
	}
	if _, err := s.downloadProviderImageSize(
		ctx, itemID, kind, providerPath, size, false, false,
	); err != nil {
		s.logger.Warn("metadata image not saved", "item_id", itemID, "kind", kind, "error", err)
	}
}

// saveDefaultArtwork saves the defaults that come from the TMDB images list:
// a textless backdrop, the top logo, and a titled backdrop as the thumb.
// detailsBackdropPath is the fallback when the list has no backdrops at all.
func (s *Service) saveDefaultArtwork(
	ctx context.Context, itemID int64, mediaType string, tmdbID int64, detailsBackdropPath string,
) {
	provided, err := s.tmdb.Images(ctx, mediaType, tmdbID)
	if err != nil {
		s.logger.Warn("TMDB artwork metadata unavailable", "item_id", itemID, "error", err)
		s.saveDefaultImage(ctx, itemID, "backdrop", detailsBackdropPath)
		return
	}
	backdropPath := defaultBackdropPath(provided.Backdrops)
	if backdropPath == "" {
		backdropPath = detailsBackdropPath
	}
	s.saveDefaultImage(ctx, itemID, "backdrop", backdropPath)
	if len(provided.Logos) > 0 {
		s.saveDefaultImage(ctx, itemID, "logo", provided.Logos[0].FilePath)
	}
	s.saveDefaultImage(ctx, itemID, "thumb", defaultThumbPath(provided.Backdrops))
}

func imageCandidates(provided tmdb.Images, kind string) ([]tmdb.ImageCandidate, string) {
	switch kind {
	case "backdrop":
		return filterBackdrops(provided.Backdrops, false), "w780"
	case "thumb":
		return filterBackdrops(provided.Backdrops, true), "w780"
	case "logo":
		return provided.Logos, "w300"
	default:
		return provided.Posters, "w342"
	}
}

// TMDB has no separate thumb artwork: a backdrop with a language has title
// art baked into the image (what Jellyfin calls a thumb), while a textless
// backdrop is a clean one suitable for backgrounds.
func filterBackdrops(backdrops []tmdb.ImageCandidate, titled bool) []tmdb.ImageCandidate {
	var result []tmdb.ImageCandidate
	for _, candidate := range backdrops {
		if (candidate.Language != "") == titled {
			result = append(result, candidate)
		}
	}
	return result
}

func defaultBackdropPath(backdrops []tmdb.ImageCandidate) string {
	if textless := filterBackdrops(backdrops, false); len(textless) > 0 {
		return textless[0].FilePath
	}
	if len(backdrops) > 0 {
		return backdrops[0].FilePath
	}
	return ""
}

func defaultThumbPath(backdrops []tmdb.ImageCandidate) string {
	if titled := filterBackdrops(backdrops, true); len(titled) > 0 {
		return titled[0].FilePath
	}
	return ""
}

func (s *Service) clearSeasonPosters(ctx context.Context, showID int64) {
	seasons, err := s.store.SeasonsForShow(ctx, showID)
	if err != nil {
		s.logger.Warn("season posters not cleared", "show_id", showID, "error", err)
		return
	}
	for _, season := range seasons {
		s.clearImage(ctx, season.ID, "poster")
	}
}

func (s *Service) clearEpisodeThumbs(ctx context.Context, showID int64) {
	episodes, err := s.store.EpisodesForShow(ctx, showID)
	if err != nil {
		s.logger.Warn("episode thumbs not cleared", "show_id", showID, "error", err)
		return
	}
	for _, episode := range episodes {
		s.clearImage(ctx, episode.ID, "thumb")
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
	if err := images.RemoveWithVariants(current.Path); err != nil {
		s.logger.Warn("old metadata image file not removed", "path", current.Path, "error", err)
	}
}

func (s *Service) downloadProviderImage(
	ctx context.Context, itemID int64, kind, providerPath string, manuallySelected, force bool,
) (*store.Image, error) {
	return s.downloadProviderImageSize(
		ctx, itemID, kind, providerPath, "original", manuallySelected, force,
	)
}

func (s *Service) downloadProviderImageSize(
	ctx context.Context, itemID int64, kind, providerPath, size string, manuallySelected, force bool,
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
	imageURL := s.tmdb.ImageURLSize(providerPath, size)
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
		_ = images.RemoveWithVariants(current.Path)
	}
	return s.store.ItemImage(ctx, itemID, kind)
}

// updateEpisodes writes TMDB episode metadata and stills for a matched show.
// missingOnly limits provider requests to seasons with missing metadata or a
// missing direct episode thumb, so complete seasons cost nothing on later scans.
func (s *Service) updateEpisodes(ctx context.Context, showID, tmdbID int64, missingOnly bool) {
	localEpisodes, err := s.store.EpisodesForShow(ctx, showID)
	if err != nil {
		s.logger.Warn("TV episode metadata not updated", "show_id", showID, "error", err)
		return
	}
	bySeason := make(map[int][]store.Item)
	needsThumb := make(map[int64]bool)
	for _, episode := range localEpisodes {
		_, imageErr := s.store.ItemImage(ctx, episode.ID, "thumb")
		missingThumb := errors.Is(imageErr, store.ErrNotFound)
		if imageErr != nil && !missingThumb {
			s.logger.Warn("episode thumb not checked", "item_id", episode.ID, "error", imageErr)
		}
		if missingOnly && episode.TMDBID != 0 && !missingThumb {
			continue
		}
		needsThumb[episode.ID] = missingThumb
		bySeason[episode.SeasonNumber] = append(bySeason[episode.SeasonNumber], episode)
	}
	for seasonNumber, items := range bySeason {
		season, err := s.tmdb.Season(ctx, tmdbID, seasonNumber)
		if err != nil {
			s.logger.Warn("TMDB season metadata unavailable", "show_id", showID,
				"season", seasonNumber, "error", err)
			continue
		}
		// backfillImages already covers season posters on the missingOnly path,
		// and an episode TMDB never lists would otherwise re-download the poster
		// on every scan.
		if !missingOnly && items[0].ParentID != nil {
			s.saveDefaultImage(ctx, *items[0].ParentID, "poster", season.PosterPath)
		}
		byNumber := make(map[int]tmdb.Episode, len(season.Episodes))
		for _, episode := range season.Episodes {
			byNumber[episode.Number] = episode
		}
		for _, item := range items {
			metadata, ok := combinedEpisodeMetadata(item, byNumber)
			if !ok {
				continue
			}
			if !missingOnly || item.TMDBID == 0 {
				if err := s.store.UpdateMetadata(ctx, item.ID, metadata); err != nil {
					s.logger.Warn("episode metadata not updated", "item_id", item.ID, "error", err)
				}
			}
			if needsThumb[item.ID] {
				// Episode stills are card artwork rather than full-screen backdrops.
				// Keeping a w780 original bounds a large TV library's storage while
				// leaving enough resolution for a high-density client thumbnail.
				s.saveDefaultImageSize(ctx, item.ID, "thumb",
					byNumber[item.EpisodeNumber].StillPath, "w780")
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

// TMDB person IDs are stable. Only producers whose work is useful as a search
// association belong here; storing every producer would make credits noisy.
var notableProducerIDs = map[int64]struct{}{
	1: {}, // George Lucas
}

// maxCast bounds how deep a cast list Loom keeps. TMDB bills fifty or more
// people on a big film, and the tail is uncredited extras that would pad every
// detail screen and match name searches nobody meant.
const maxCast = 15

// storeCredits puts directors and the curated producer credit ahead of the
// cast, which is the order a detail screen reads them in and the store preserves.
func storeCredits(
	cast []tmdb.CastCredit, directors []tmdb.Director, producers []tmdb.Producer,
) []store.Credit {
	credits := make([]store.Credit, 0, len(directors)+1+min(len(cast), maxCast))
	creditedPeople := make(map[int64]struct{}, len(directors))
	for _, director := range directors {
		credits = append(credits, store.Credit{
			PersonID: director.ID, Name: director.Name, Role: "director",
		})
		creditedPeople[director.ID] = struct{}{}
	}
	for _, producer := range producers {
		if _, notable := notableProducerIDs[producer.ID]; !notable {
			continue
		}
		if _, alreadyCredited := creditedPeople[producer.ID]; alreadyCredited {
			continue
		}
		credits = append(credits, store.Credit{
			PersonID: producer.ID, Name: producer.Name, Role: "producer",
		})
		creditedPeople[producer.ID] = struct{}{}
	}
	for index, member := range cast {
		if index == maxCast {
			break
		}
		credits = append(credits, store.Credit{
			PersonID: member.ID, Name: member.Name, Role: "actor", Character: member.Character,
		})
	}
	return credits
}

func storeGenres(genres []tmdb.Genre) []store.Genre {
	result := make([]store.Genre, len(genres))
	for index, genre := range genres {
		result[index] = store.Genre{ID: genre.ID, Name: genre.Name}
	}
	return result
}

func metadataType(kind string) string {
	if kind == "show" {
		return "tv"
	}
	return "movie"
}

func selectAutomaticMatch(matches []tmdb.SearchResult) (*tmdb.SearchResult, string) {
	if len(matches) == 0 {
		return nil, "no exact title and year candidates"
	}
	if len(matches) == 1 {
		return &matches[0], ""
	}

	best := 0
	secondHighestVotes := 0
	for i := 1; i < len(matches); i++ {
		if matches[i].VoteCount > matches[best].VoteCount ||
			(matches[i].VoteCount == matches[best].VoteCount && matches[i].VoteAverage > matches[best].VoteAverage) {
			secondHighestVotes = max(secondHighestVotes, matches[best].VoteCount)
			best = i
		} else {
			secondHighestVotes = max(secondHighestVotes, matches[i].VoteCount)
		}
	}
	selected := &matches[best]
	if selected.VoteCount < automaticMatchMinVotes || selected.VoteAverage < automaticMatchMinVoteAverage {
		return nil, "leading candidate is below the vote threshold"
	}
	if secondHighestVotes > 0 && selected.VoteCount < secondHighestVotes*automaticMatchVoteDominanceFactor {
		return nil, "leading candidate does not clearly dominate the alternatives"
	}
	return selected, ""
}

func normalizedTitle(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, value)
}

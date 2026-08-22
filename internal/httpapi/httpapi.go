package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/five82/loom/internal/collections"
	"github.com/five82/loom/internal/images"
	"github.com/five82/loom/internal/library"
	"github.com/five82/loom/internal/metadata"
	"github.com/five82/loom/internal/store"
)

type ListenAddresses struct {
	API     []string `json:"api"`
	Control string   `json:"control"`
}

type API struct {
	store     *store.Store
	scans     *library.Manager
	metadata  *metadata.Service
	shutdown  chan<- struct{}
	listeners ListenAddresses
	public    *http.ServeMux
}

func New(catalog *store.Store, scans *library.Manager, metadataService *metadata.Service,
	shutdown chan<- struct{}, listeners ListenAddresses,
) *API {
	api := &API{
		store: catalog, scans: scans, metadata: metadataService,
		shutdown: shutdown, listeners: listeners, public: http.NewServeMux(),
	}
	api.public.HandleFunc("GET /api/v1/health", api.health)
	api.public.HandleFunc("GET /api/v1/libraries", api.libraries)
	api.public.HandleFunc("GET /api/v1/genres", api.genres)
	api.public.HandleFunc("GET /api/v1/collections", api.collections)
	api.public.HandleFunc("GET /api/v1/featured-pick", api.featuredPick)
	api.public.HandleFunc("GET /api/v1/search", api.search)
	api.public.HandleFunc("GET /api/v1/items", api.items)
	api.public.HandleFunc("GET /api/v1/items/{id}", api.item)
	api.public.HandleFunc("GET /api/v1/items/{id}/children", api.children)
	api.public.HandleFunc("GET /api/v1/items/{id}/playback", api.playback)
	api.public.HandleFunc("PUT /api/v1/items/{id}/progress", api.saveProgress)
	api.public.HandleFunc("POST /api/v1/items/{id}/played", api.markPlayed)
	api.public.HandleFunc("DELETE /api/v1/items/{id}/played", api.clearPlayed)
	api.public.HandleFunc("GET /api/v1/items/{id}/images/{kind}/options", api.imageOptions)
	api.public.HandleFunc("PUT /api/v1/items/{id}/images/{kind}", api.selectImage)
	api.public.HandleFunc("POST /api/v1/items/{id}/images/{kind}/reset", api.resetImage)
	api.public.HandleFunc("GET /api/v1/media/{id}", api.media)
	api.public.HandleFunc("GET /api/v1/images/{id}", api.image)
	api.public.HandleFunc("GET /api/v1/continue-watching", api.continueWatching)
	api.public.HandleFunc("GET /api/v1/next-up", api.nextUp)
	api.public.HandleFunc("GET /api/v1/recently-added", api.recentlyAdded)
	api.public.HandleFunc("GET /api/v1/recently-played", api.recentlyPlayed)
	// Scanning is public so a client or an off-host ingest workflow can pick up new
	// files without waiting for the scheduled scan. A scan is incremental and the
	// manager runs at most one at a time, so an unauthenticated caller cannot do
	// more than keep a single scan running.
	api.public.HandleFunc("POST /api/v1/scan", api.scan)
	api.public.HandleFunc("GET /api/v1/scan", api.scanStatus)
	return api
}

func (a *API) PublicHandler() http.Handler { return recoverMiddleware(a.public) }

// LocalHandler adds daemon control operations that must never be exposed over LAN.
func (a *API) LocalHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_loom/status", a.status)
	mux.HandleFunc("POST /_loom/stop", a.stop)
	mux.HandleFunc("GET /_loom/unmatched", a.unmatched)
	mux.HandleFunc("GET /_loom/metadata/search", a.metadataSearch)
	mux.HandleFunc("POST /_loom/metadata/match", a.metadataMatch)
	mux.Handle("/", a.public)
	return recoverMiddleware(mux)
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) libraries(w http.ResponseWriter, r *http.Request) {
	libraries, err := a.store.Libraries(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": libraries})
}

func (a *API) genres(w http.ResponseWriter, r *http.Request) {
	genres, err := a.store.Genres(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": genres})
}

// collections serves every shelf with its members resolved, rather than a
// summary plus a request per shelf, because a client drawing the collections
// row needs the posters immediately and the whole payload is a few hundred
// movies. Dynamic shelves are resolved from the current catalog before the
// hand-picked shelves. A shelf is dropped when fewer than two of its members
// are owned: one movie under a heading is worse than leaving it in the grid.
func (a *API) collections(w http.ResponseWriter, r *http.Request) {
	type collection struct {
		Slug  string       `json:"slug"`
		Title string       `json:"title"`
		Items []store.Item `json:"items"`
	}
	result := make([]collection, 0, len(collections.All)+2)
	today := time.Now().UTC()
	newReleases, err := a.store.ItemsReleasedBetween(r.Context(), today.AddDate(0, -18, 0), today)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(newReleases) >= 2 {
		result = append(result, collection{Slug: "new-releases", Title: "New Releases", Items: newReleases})
	}
	hdr, err := a.store.ItemsByVideoDynamicRange(r.Context(), []string{"hdr", "dolby_vision"})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(hdr) >= 2 {
		result = append(result, collection{Slug: "hdr", Title: "HDR", Items: hdr})
	}
	for _, defined := range collections.All {
		items, err := a.store.ItemsByTMDBID(r.Context(), defined.TMDBIDs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if len(items) < 2 {
			continue
		}
		result = append(result, collection{Slug: defined.Slug, Title: defined.Title, Items: items})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": result})
}

func (a *API) featuredPick(w http.ResponseWriter, r *http.Request) {
	pick, err := a.store.FeaturedPickAt(r.Context(), time.Now())
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "featured pick unavailable")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pick)
}

func (a *API) search(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeError(w, http.StatusBadRequest, "q must not be empty")
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	items, fuzzy, err := a.store.SearchItems(r.Context(), query, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "limit": limit, "offset": offset, "fuzzy": fuzzy,
	})
}

func (a *API) items(w http.ResponseWriter, r *http.Request) {
	libraryKind := r.URL.Query().Get("library")
	if libraryKind != "" && libraryKind != "movies" && libraryKind != "shorts" && libraryKind != "tv" {
		writeError(w, http.StatusBadRequest, "library must be movies, shorts, or tv")
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	opts := store.ListOptions{
		LibraryKind: libraryKind, Kind: r.URL.Query().Get("kind"), Limit: limit, Offset: offset,
	}
	if genre := r.URL.Query().Get("genre_id"); genre != "" {
		id, err := strconv.ParseInt(genre, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "genre_id must be a positive integer")
			return
		}
		opts.GenreID = id
	}
	if parent := r.URL.Query().Get("parent_id"); parent != "" {
		id, err := strconv.ParseInt(parent, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "parent_id must be a positive integer")
			return
		}
		opts.ParentID = &id
	} else {
		opts.TopLevel = true
	}
	items, err := a.store.ListItems(r.Context(), opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
}

func (a *API) item(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	item, err := a.store.Item(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "item not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *API) children(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := a.store.Item(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "item not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	items, err := a.store.ListItems(r.Context(), store.ListOptions{ParentID: &id, Limit: limit, Offset: offset})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
}

func (a *API) playback(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	item, err := a.store.Item(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "item not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if item.Media == nil {
		writeError(w, http.StatusConflict, "item is not directly playable")
		return
	}
	// Report the size and tag observed on disk rather than the scanner's recorded
	// values. The two disagree whenever a file changes before the next scan, and a
	// stale tag would make the media handler reject the very stream URL returned
	// here. Recomputing keeps a tag mismatch meaning only what it should: the file
	// changed after a client started downloading it.
	info, err := os.Stat(item.Media.Path)
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "media file is unavailable")
		return
	}
	item.Media.Size = info.Size()
	item.Media.Tag = store.MediaTag(item.Media.ID, info.Size(), info.ModTime().UnixNano())
	writeJSON(w, http.StatusOK, map[string]any{
		"item_id": item.ID, "media": item.Media,
		"stream_url": fmt.Sprintf("/api/v1/media/%d?tag=%s", item.Media.ID, item.Media.Tag),
	})
}

func (a *API) saveProgress(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var request struct {
		PositionMS int64 `json:"position_ms"`
		DurationMS int64 `json:"duration_ms"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid progress body: "+err.Error())
		return
	}
	progress, err := a.store.SetProgress(r.Context(), id, request.PositionMS, request.DurationMS)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "playable item not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, progress)
}

// markPlayed and clearPlayed accept a movie, episode, season, or show, so a
// viewer can retire a whole series in one call. Both report how many playback
// rows changed; addressing something with no playable media below it is not an
// error, it simply changes nothing.
func (a *API) markPlayed(w http.ResponseWriter, r *http.Request) {
	a.writePlayed(w, r, a.store.SetPlayed)
}

func (a *API) clearPlayed(w http.ResponseWriter, r *http.Request) {
	a.writePlayed(w, r, a.store.ClearPlayback)
}

func (a *API) writePlayed(w http.ResponseWriter, r *http.Request, apply func(context.Context, int64) (int64, error)) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	updated, err := apply(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "item not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"updated": updated})
}

func (a *API) media(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	media, err := a.store.Media(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "media not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	file, err := os.Open(media.Path)
	if err != nil {
		writeError(w, http.StatusNotFound, "media file is unavailable")
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "media file is unavailable")
		return
	}
	actualTag := store.MediaTag(media.ID, info.Size(), info.ModTime().UnixNano())
	requestedTag := r.URL.Query().Get("tag")
	if requestedTag != "" && requestedTag != actualTag {
		writeError(w, http.StatusNotFound, "media version not found")
		return
	}
	if contentType := mediaContentType(media.Path); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("ETag", strconv.Quote(actualTag))
	if requestedTag == actualTag {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, filepath.Base(media.Path), info.ModTime(), file)
}

func (a *API) imageOptions(w http.ResponseWriter, r *http.Request) {
	if a.metadata == nil {
		writeError(w, http.StatusServiceUnavailable, "TMDB metadata is disabled")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	kind, ok := imageKind(w, r)
	if !ok {
		return
	}
	options, err := a.metadata.ImageOptions(r.Context(), id, kind)
	if err != nil {
		a.writeImageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": options})
}

func (a *API) selectImage(w http.ResponseWriter, r *http.Request) {
	if a.metadata == nil {
		writeError(w, http.StatusServiceUnavailable, "TMDB metadata is disabled")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	kind, ok := imageKind(w, r)
	if !ok {
		return
	}
	var request struct {
		Provider     string `json:"provider"`
		ProviderPath string `json:"provider_path"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid image selection: "+err.Error())
		return
	}
	image, err := a.metadata.SelectImage(r.Context(), id, kind, request.Provider, request.ProviderPath)
	if err != nil {
		a.writeImageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, image)
}

func (a *API) resetImage(w http.ResponseWriter, r *http.Request) {
	if a.metadata == nil {
		writeError(w, http.StatusServiceUnavailable, "TMDB metadata is disabled")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	kind, ok := imageKind(w, r)
	if !ok {
		return
	}
	image, err := a.metadata.ResetImage(r.Context(), id, kind)
	if err != nil {
		a.writeImageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, image)
}

func (a *API) writeImageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "item not found")
	case errors.Is(err, metadata.ErrImageOptionNotFound):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, metadata.ErrImageUnavailable):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusBadGateway, err.Error())
	}
}

func imageKind(w http.ResponseWriter, r *http.Request) (string, bool) {
	kind := r.PathValue("kind")
	if kind != "poster" && kind != "backdrop" && kind != "logo" && kind != "thumb" {
		writeError(w, http.StatusBadRequest, "image kind must be poster, backdrop, logo, or thumb")
		return "", false
	}
	return kind, true
}

func (a *API) image(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	image, err := a.store.Image(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "image not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	servePath := image.Path
	if value := r.URL.Query().Get("width"); value != "" {
		requested, err := strconv.Atoi(value)
		if err != nil || requested <= 0 {
			writeError(w, http.StatusBadRequest, "width must be a positive integer")
			return
		}
		variant, err := images.Variant(image.Path, images.SnapWidth(requested))
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "image file is unavailable")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "image resize failed: "+err.Error())
			return
		}
		servePath = variant
	}
	file, err := os.Open(servePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "image file is unavailable")
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "image file is unavailable")
		return
	}
	// A stale tag is not an error: clients hold item rows with the tag baked
	// into artwork URLs, and after an artwork change a client that hasn't
	// refetched its rows still asks with the old tag. Answering 404 left
	// those clients rendering blank artwork, so serve the current image
	// instead; the tag only decides cacheability below (matching tag is
	// immutable, anything else must revalidate).
	requestedTag := r.URL.Query().Get("tag")
	contentType := image.ContentType
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(image.Path))
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if image.Tag != "" {
		w.Header().Set("ETag", strconv.Quote(image.Tag))
	}
	if requestedTag == image.Tag && image.Tag != "" {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, filepath.Base(servePath), info.ModTime(), file)
}

func mediaContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mkv":
		return "video/x-matroska"
	case ".avi":
		return "video/x-msvideo"
	case ".webm":
		return "video/webm"
	case ".mpg", ".mpeg":
		return "video/mpeg"
	}
	return mime.TypeByExtension(filepath.Ext(path))
}

func (a *API) continueWatching(w http.ResponseWriter, r *http.Request) {
	limit, _, err := pagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	items, err := a.store.ContinueWatching(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) nextUp(w http.ResponseWriter, r *http.Request) {
	limit, _, err := pagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	items, err := a.store.NextUp(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) recentlyAdded(w http.ResponseWriter, r *http.Request) {
	limit, _, err := pagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	items, err := a.store.RecentlyAdded(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) recentlyPlayed(w http.ResponseWriter, r *http.Request) {
	limit, _, err := pagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	items, err := a.store.RecentlyPlayed(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) status(w http.ResponseWriter, r *http.Request) {
	stats, err := a.store.Stats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	lastScans, err := a.store.LastScans(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"running": true, "pid": os.Getpid(), "scan": a.scans.Status(),
		"listeners": a.listeners, "catalog": stats, "last_scans": lastScans,
	})
}

func (a *API) stop(w http.ResponseWriter, _ *http.Request) {
	select {
	case a.shutdown <- struct{}{}:
	default:
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "stopping"})
}

func (a *API) scan(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Library string `json:"library"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid scan body: "+err.Error())
			return
		}
	}
	if request.Library != "" && request.Library != "movies" && request.Library != "shorts" && request.Library != "tv" {
		writeError(w, http.StatusBadRequest, "library must be movies, shorts, tv, or empty")
		return
	}
	if !a.scans.Trigger(request.Library) {
		writeError(w, http.StatusConflict, "a library scan is already running")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started", "library": request.Library})
}

func (a *API) scanStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.scans.Status())
}

func (a *API) unmatched(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.UnmatchedItems(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) metadataSearch(w http.ResponseWriter, r *http.Request) {
	if a.metadata == nil {
		writeError(w, http.StatusServiceUnavailable, "TMDB metadata is disabled")
		return
	}
	mediaType := r.URL.Query().Get("type")
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if (mediaType != "movie" && mediaType != "tv") || query == "" {
		writeError(w, http.StatusBadRequest, "type must be movie or tv and query must not be empty")
		return
	}
	year := 0
	if value := r.URL.Query().Get("year"); value != "" {
		var err error
		year, err = strconv.Atoi(value)
		if err != nil || year < 1800 || year > 3000 {
			writeError(w, http.StatusBadRequest, "year must be between 1800 and 3000")
			return
		}
	}
	results, err := a.metadata.Search(r.Context(), mediaType, query, year)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": results})
}

func (a *API) metadataMatch(w http.ResponseWriter, r *http.Request) {
	if a.metadata == nil {
		writeError(w, http.StatusServiceUnavailable, "TMDB metadata is disabled")
		return
	}
	var request struct {
		ItemID int64 `json:"item_id"`
		TMDBID int64 `json:"tmdb_id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.ItemID <= 0 || request.TMDBID <= 0 {
		writeError(w, http.StatusBadRequest, "item_id and tmdb_id must be positive integers")
		return
	}
	if err := a.metadata.Match(r.Context(), request.ItemID, request.TMDBID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "item not found")
		} else {
			writeError(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "matched"})
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "id must be a positive integer")
		return 0, false
	}
	return id, true
}

func pagination(r *http.Request) (int, int, error) {
	limit := 50
	offset := 0
	var err error
	if value := r.URL.Query().Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 200 {
			return 0, 0, fmt.Errorf("limit must be between 1 and 200")
		}
	}
	if value := r.URL.Query().Get("offset"); value != "" {
		offset, err = strconv.Atoi(value)
		if err != nil || offset < 0 {
			return 0, 0, fmt.Errorf("offset must be non-negative")
		}
	}
	return limit, offset, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

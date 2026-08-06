package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/five82/loom/internal/images"
	"github.com/five82/loom/internal/library"
	"github.com/five82/loom/internal/metadata"
	"github.com/five82/loom/internal/store"
)

type API struct {
	store    *store.Store
	scans    *library.Manager
	metadata *metadata.Service
	shutdown chan<- struct{}
	public   *http.ServeMux
}

func New(catalog *store.Store, scans *library.Manager, metadataService *metadata.Service, shutdown chan<- struct{}) *API {
	api := &API{
		store: catalog, scans: scans, metadata: metadataService,
		shutdown: shutdown, public: http.NewServeMux(),
	}
	api.public.HandleFunc("GET /api/v1/health", api.health)
	api.public.HandleFunc("GET /api/v1/libraries", api.libraries)
	api.public.HandleFunc("GET /api/v1/genres", api.genres)
	api.public.HandleFunc("GET /api/v1/search", api.search)
	api.public.HandleFunc("GET /api/v1/items", api.items)
	api.public.HandleFunc("GET /api/v1/items/{id}", api.item)
	api.public.HandleFunc("GET /api/v1/items/{id}/children", api.children)
	api.public.HandleFunc("GET /api/v1/items/{id}/playback", api.playback)
	api.public.HandleFunc("PUT /api/v1/items/{id}/progress", api.saveProgress)
	api.public.HandleFunc("GET /api/v1/items/{id}/images/{kind}/options", api.imageOptions)
	api.public.HandleFunc("PUT /api/v1/items/{id}/images/{kind}", api.selectImage)
	api.public.HandleFunc("POST /api/v1/items/{id}/images/{kind}/reset", api.resetImage)
	api.public.HandleFunc("GET /api/v1/media/{id}", api.media)
	api.public.HandleFunc("GET /api/v1/images/{id}", api.image)
	api.public.HandleFunc("GET /api/v1/continue-watching", api.continueWatching)
	api.public.HandleFunc("GET /api/v1/recently-added", api.recentlyAdded)
	return api
}

func (a *API) PublicHandler() http.Handler { return recoverMiddleware(a.public) }

// LocalHandler adds daemon control operations that must never be exposed over LAN.
func (a *API) LocalHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_loom/status", a.status)
	mux.HandleFunc("POST /_loom/stop", a.stop)
	mux.HandleFunc("POST /_loom/scan", a.scan)
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
	items, err := a.store.SearchItems(r.Context(), query, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
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
	requestedTag := r.URL.Query().Get("tag")
	if requestedTag != "" && requestedTag != image.Tag {
		writeError(w, http.StatusNotFound, "image version not found")
		return
	}
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

func (a *API) status(w http.ResponseWriter, r *http.Request) {
	stats, err := a.store.Stats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"running": true, "pid": os.Getpid(), "scan": a.scans.Status(), "catalog": stats,
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

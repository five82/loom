# Loom

Loom is a personal, single-user movie, short film, and TV media server. It
catalogs media from read-only libraries and serves original files directly to
clients. Loom intentionally never transcodes or remuxes media.

The initial client will be an Android/Android TV application. Loom currently
has no authentication, so it should only be used on a trusted LAN.

## Requirements

- Linux
- `ffprobe` available in `PATH`
- Read access to the configured movie, short film, and TV libraries

Build the Loom executable as a static binary with:

```bash
CGO_ENABLED=0 go build -trimpath -o loom ./cmd/loom
```

`ffprobe` is the only runtime command Loom invokes.

## Configuration

Create the default XDG configuration file:

```bash
loom config init
# Edit the path printed by the command.
loom config validate
```

The default configuration is:

```toml
[api]
bind = "0.0.0.0:8097"

[paths]
state_dir = "~/.local/state/loom"

[library]
movies_dir = "/media/daspool/media/content/movies"
shorts_dir = "/media/daspool/media/content/shorts"
tv_dir = "/media/daspool/media/content/tv"

[scanner]
# Set to "0" to disable scheduled scans. Loom does not scan at startup.
interval = "24h"

[tmdb]
api_key = ""
language = "en-US"
```

Use `--config /path/to/config.toml` to select another file. `TMDB_API_KEY`
overrides the configured key. When the key is empty, media scanning and direct
play remain available but TMDB matching is disabled.

Runtime data is kept under `paths.state_dir`:

```text
loom.db             SQLite catalog and playback state
daemon.log          structured daemon log
images/             selected TMDB poster, backdrop, logo, and thumb originals
                    plus cached resized variants
```

The local control socket and daemon lock use `$XDG_RUNTIME_DIR`, with `/tmp` as
a fallback.

## Running

```bash
loom start
loom status
loom logs --follow
loom stop
```

`loom start` launches a detached daemon and waits for its Unix control socket
to become ready. `loom stop` requests a graceful shutdown over that socket. A
PID file is not used.

To return Loom to a clean state during development:

```bash
loom developer reset
```

This stops the daemon if necessary and deletes everything under
`paths.state_dir`, including the database, catalog, playback state, metadata,
daemon logs, and downloaded artwork. Media libraries are not modified. The
loaded `config.toml` is also preserved, even when it is stored inside the state
directory. This is a destructive development tool, not the normal schema
upgrade path.

For manual scans:

```bash
loom scan            # movies, short films, then TV
loom scan movies
loom scan shorts
loom scan tv
```

Only one manual or scheduled scan runs at a time. Scans are incremental:
unchanged files are not probed again. New movies and shows are automatically
matched when TMDB returns one exact title/year match, or when one exact match
clearly dominates duplicate results by TMDB votes. Scans also backfill missing
poster, backdrop, logo, and TV season poster artwork for existing TMDB matches.

Review and correct uncertain matches from the CLI:

```bash
loom unmatched
loom search movie "Arrival" --year 2016
loom search tv "Batman"
loom match ITEM_ID TMDB_ID
```

A match stores provider metadata, including movie genres, in SQLite and
downloads the default poster, backdrop, and TMDB's preferred logo into Loom's
state directory when they are available. TV matches also populate metadata for
local `SxxEyy` episodes and download season posters. Clients can browse TMDB
poster, backdrop, and logo
options, select another image, or reset the selection to TMDB's default or
preferred option. Season items support the same selection flow for posters.
Manual choices survive metadata refreshes and are reset only when the item is
matched to a different TMDB title. Loom never writes artwork into the media
libraries.

## Library conventions

Movies and short films use the same layout: one first-level directory containing
exactly one video file directly inside it:

```text
movies/
  Arrival (2016)/
    Arrival (2016).mkv
shorts/
  Presto (2008)/
    Presto (2008).mkv
```

Nested movie and short film videos are ignored, including anything under
`extras/` or `behindthescenes/`. Short films use TMDB movie metadata while
remaining a separate Loom library.

TV show directories may be flat or contain season directories. Episode numbers
come from the filename:

```text
tv/
  The Office (US)/
    Season 4/
      The Office (US) - S04E01-02 - Fun Run.mkv
```

Loom recognizes `SxxEyy` and `SxxEyy-zz`, including season zero specials and
season numbers longer than two digits. Videos without an episode identifier are
cataloged as unmatched.

NFO files, local artwork, external subtitle files, and movie extras are ignored.
Embedded stream details are reported from `ffprobe`, including video and audio
codecs, resolution, HDR/Dolby Vision classification, audio channel layout, and
subtitle tracks.

## HTTP API

The unauthenticated LAN API is rooted at `/api/v1`:

```text
GET  /api/v1/health
GET  /api/v1/libraries
GET  /api/v1/genres
GET  /api/v1/search?q=pilot
GET  /api/v1/items?library=movies
GET  /api/v1/items?library=shorts
GET  /api/v1/items?library=movies&genre_id=878
GET  /api/v1/items/{id}
GET  /api/v1/items/{id}/children
GET  /api/v1/items/{id}/playback
GET  /api/v1/items/{id}/images/{poster|backdrop|logo|thumb}/options
PUT  /api/v1/items/{id}/images/{poster|backdrop|logo|thumb}
POST /api/v1/items/{id}/images/{poster|backdrop|logo|thumb}/reset
GET  /api/v1/media/{media-id}
GET  /api/v1/images/{image-id}?tag={image-tag}&width={pixels}
PUT  /api/v1/items/{id}/progress
GET  /api/v1/continue-watching
GET  /api/v1/recently-added
```

Media responses support `HEAD` and HTTP byte ranges. Playback responses include
the original filename, exact file size in bytes, and a media version tag. The
returned stream URL includes that tag; tagged responses have an `ETag` and are
immutable, while stale tags return `404 Not Found`. This lets clients resume and
validate offline downloads without combining bytes from different file
versions. An offline download is always a full-size copy of the original source
because Loom never transcodes, remuxes, or creates quality variants. The API
never accepts a filesystem path from a client.

Select one of the provider paths returned by the image-options endpoint:

```json
{
  "provider": "tmdb",
  "provider_path": "/abc123.jpg"
}
```

The genres endpoint lists movie and short film genres represented in the
available catalog, including an item count. Movie items include their genres and
can be filtered by TMDB genre ID.

Search matches available movie, show, and episode titles case-insensitively.
Exact and prefix matches are returned before other substring matches. Results
support `limit` and `offset`; episode results include `series_title` and
`season_title` for display outside their hierarchy.

Items expose poster, backdrop, logo, and thumb image IDs with content tags.
Clients should include the tag query parameter when fetching an image; tagged
responses are immutable and a changed selection produces a new tag. Seasons use
their TMDB season poster when available and otherwise inherit the show poster.
Episodes inherit the season poster; season and episode backdrops, logos, and
thumbs inherit from the show. Image selection is unauthenticated under
Loom's trusted-LAN model.

Backdrops are textless TMDB backdrops; thumbs are the language-tagged TMDB
backdrops that have title art baked in (the same source Jellyfin uses for its
thumb artwork). The optional `width` query parameter serves a resized copy,
snapped up to fixed buckets of 240, 480, 960, or 1440 pixels so a phone never
decodes a full-size original for a small card. Variants are resized once and
cached on disk; requests at or above the original width return the original.

Progress requests use milliseconds:

```json
{
  "position_ms": 120000,
  "duration_ms": 3600000
}
```

Items from 5% through 90% are offered for resume when their duration is at least
five minutes. At 90% they are marked played, accounting for end credits.

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

To ship the working tree to the running server:

```bash
./deploy.sh
```

This snapshots the catalog, builds a static binary, stops the daemon, installs
over the `loom` on `PATH` while keeping the previous binary beside it, and
starts again. The daemon has to be down before the new binary lands because a
schema change migrates on startup. The script does not test what it ships, so
run `./check-ci.sh` first, and after a schema change verify the migration by
hand before trusting the result.

To snapshot the catalog before a risky change:

```bash
loom backup [path]
```

This writes a consistent copy of `loom.db` with SQLite's `VACUUM INTO` and
prints its path, so the daemon does not have to be stopped and a running scan is
not disturbed. The snapshot is created with mode `0600` and an existing file is
never overwritten. Without a path it lands in a timestamped file under `/tmp`,
which is fine for a pre-migration safety copy but is not durable backup storage.
Copying `loom.db` by hand is not equivalent: WAL mode keeps recent commits in a
separate `loom.db-wal` file.

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

Scans can also be started over the LAN API, so a client or an ingest workflow on
another host can pick up new files without waiting for the scheduled scan:

```bash
curl -X POST http://loom:8097/api/v1/scan -d '{"library":"movies"}'
curl http://loom:8097/api/v1/scan
```

An empty or omitted body scans every library. The trigger returns `202 Accepted`
and the scan runs in the background; poll `GET /api/v1/scan` for the running
library, start and end times, and the last error. Triggering a scan while one is
running returns `409 Conflict`.

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

Replacing a file with a new encode is expected. A movie is identified by its
first-level directory name and an episode by its season and episode numbers, so
a replacement file can be named anything and the TMDB match, artwork selection,
and playback state carry over. Renaming a movie directory, or changing an
episode's numbers, creates a new item and leaves the old state behind. Unmatched
videos are still identified by filename because they have no episode numbers to
key on.

The old file does not have to disappear at the same instant the new one lands.
While several videos share a movie directory, or claim one episode, Loom uses
the most recently modified file and logs the ones it passed over, so a title
stays in the catalog and keeps playing throughout the swap. Delete the leftover
when convenient; the next scan then finds a single file and changes nothing.

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
POST /api/v1/items/{id}/played
DELETE /api/v1/items/{id}/played
GET  /api/v1/continue-watching
GET  /api/v1/next-up
GET  /api/v1/recently-added
POST /api/v1/scan
GET  /api/v1/scan
```

Media responses support `HEAD` and HTTP byte ranges. Playback responses include
the original filename, exact file size in bytes, and a media version tag. The
size and tag describe the file as it is on disk when the request is served
rather than as the last scan recorded it, so a playback response always returns
a stream URL the media endpoint accepts.
That stream URL includes the tag; tagged responses have an `ETag` and are
immutable, while stale tags return `404 Not Found`. This lets clients resume and
validate offline downloads without combining bytes from different file
versions. An offline download is always a full-size copy of the original source
because Loom never transcodes, remuxes, or creates quality variants. The API
never accepts a filesystem path from a client.

Item and playback responses carry the `chapters` embedded in the file, each with
a start offset in milliseconds and, when the container names it, a title. Disc
rips routinely leave the marks unnamed, so a client should be ready to label a
chapter by its number alone. Files with no chapters, and files whose single
chapter spans the whole runtime, omit the field because neither offers anywhere
to navigate to.

Items also carry `media_tag`, the file version recorded by the last scan. A
replacement encode changes nothing else about an item, so without it a client
holding an offline copy cannot tell that the file behind a title changed short of
requesting playback details for every item. The playback endpoint reads the file
as it is on disk and stays authoritative; `media_tag` lags until the next scan.

Continue Watching and Next Up together keep a show on the home screen for a
whole binge. Continue Watching returns only partially watched items, so an
episode leaves it once playback passes the played threshold; Next Up then offers
the earliest unfinished episode of every show with viewing history. The two rows
never list the same show, because a show with a resumable episode stays out of
Next Up. An episode sampled below the resume floor reaches neither row on its
own, so Next Up keeps offering it rather than skipping ahead. Specials are
excluded from Next Up so season zero cannot stand in front of a pilot. Both
return items in the same shape.

Playback progress is not only reported by a player. `POST` to an item's `played`
endpoint records it as watched without playing it, for a title finished on
another device, and `DELETE` forgets its playback state entirely, which both
retires an abandoned title from Continue Watching and returns a series to a
first watch. Both accept a movie, episode, season, or show and cascade to every
playable item beneath it, and both answer with the number of playback records
they changed.

Browse listings carry `progress` for any item that has been played, so a client
showing a season can mark watched episodes without a request per episode. Items
with no playback history omit the field.

Shows and seasons carry `episode_count` and `unwatched_count`, counting only
episodes that are still on disk, so a grid can badge a series with what is left
to watch without walking down to every episode. Both fields are omitted where
they would be zero, so a fully watched show reports no `unwatched_count` and
movies and episodes report neither.

Select one of the provider paths returned by the image-options endpoint:

```json
{
  "provider": "tmdb",
  "provider_path": "/abc123.jpg"
}
```

The genres endpoint lists the genres represented in the available movie library,
including an item count. Short films are browsed as their own top-level library
rather than by genre, so they are excluded from these counts. Items include
their genres and can be filtered by TMDB genre ID.

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

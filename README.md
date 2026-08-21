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
name = "Loom"

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

Use `--config /path/to/config.toml` to select another file. `name` is the
instance name advertised to Takeup over local-network discovery; give each Loom
server a distinct name such as `Loom` or `Loom Test`. `TMDB_API_KEY` overrides
the configured key. When the key is empty, media scanning and direct play remain
available but TMDB matching is disabled.

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

Without further setup, `loom start` launches a detached daemon and waits for
its Unix control socket to become ready. `loom stop` requests a graceful
shutdown over that socket. A PID file is not used.

Loom can optionally install a systemd user service:

```bash
loom service install
```

This requires a config file, writes `loom.service` under the systemd user unit
directory, enables it, moves any running detached daemon under systemd, and
starts it. The unit records the current Loom executable, config file, and
`ffprobe` location, so uninstall and reinstall it after moving any of them.
Installation is opt-in; Loom never installs the service during a build or
deployment.

The usual `loom start`, `loom stop`, and `loom restart` commands automatically
use systemd while the service is installed. Remove it and return to detached
operation with:

```bash
loom service uninstall
```

A systemd user service normally starts when its user logs in. To start Loom
during boot before login, enable lingering once for that user:

```bash
sudo loginctl enable-linger "$USER"
```

`loom service install` reports whether this step is needed. Loom's structured
application log remains available through `loom logs`; startup failures are
also available through `journalctl --user -u loom.service`.

To ship the working tree to the running server:

```bash
./deploy.sh
```

This builds a static binary, audits and stops the daemon, takes an exact catalog
snapshot, installs over the `loom` on `PATH` while keeping the previous binary
beside it, runs any pending schema migrations, verifies catalog integrity and
preserved-state counts, and starts again. The script does not test what it
ships, so run `./check-ci.sh` first. Deploy the same commit to the test instance
before production.

Schema upgrades are explicit and must run while Loom is stopped:

```bash
loom stop
loom migrate
loom start
```

`loom migrate` creates a missing database at the current schema, does nothing
when the schema is already current, and otherwise applies each pending migration
in its own transaction. Normal daemon startup refuses a database with pending
migrations. Migrations are retained so the test and production databases, as
well as older backups, can be upgraded later. `deploy.sh` performs this sequence
and should normally be used instead of invoking it by hand. Keep the test
catalog between deployments, or periodically replace it with a production
snapshot, so schema testing does not require re-fetching all TMDB metadata. Use
`loom developer reset` only when deliberately testing a clean bootstrap.

To snapshot the catalog before a risky change:

```bash
loom backup [path]
```

This writes a consistent copy of `loom.db` with SQLite's `VACUUM INTO` and
prints its path, so the daemon does not have to be stopped and a running scan is
not disturbed. The snapshot is created with mode `0600` and an existing file is
never overwritten. Without a path it lands in a timestamped file under
`paths.state_dir/backups`; pass a path to store it elsewhere. Copying `loom.db`
by hand is not equivalent: WAL mode keeps recent commits in a separate
`loom.db-wal` file. `deploy.sh` stops Loom before taking its snapshot so the
snapshot is an exact rollback point.

To check the catalog for problems:

```bash
loom developer audit
loom developer audit --json
```

The audit reads the database directly and writes nothing, so it runs whether or
not the daemon is up. It reports the schema version plus playback-state and
manual-artwork counts used to validate migrations, followed by two kinds of
finding. Integrity checks describe
states that should never occur — foreign key violations, a playable item with no
media file, a probe that failed or reported no duration, a season or episode
whose parent is wrong, a director credited on something that is not a movie, an
artwork row pointing at a file that is gone — and the command exits non-zero
when any of them match. Metadata checks describe what TMDB did not supply, such
as a short film with no billed cast or an episode whose local numbering the
provider disagrees with. Those are routine, so they are reported without failing
the command. Every match is listed, because how a finding is distributed is
usually the thing worth seeing.

Run the audit once a scan has finished, because a scan in progress moves items
through these states legitimately.

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
poster, backdrop, logo, TV season poster, and episode still artwork for existing
TMDB matches.

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
local `SxxEyy` episodes and download season posters and episode stills. Clients
can browse TMDB poster, backdrop, and logo
options, select another image, or reset the selection to TMDB's default or
preferred option. Season items support the same selection flow for posters.
Manual choices survive metadata refreshes and are reset only when the item is
matched to a different TMDB title. Loom never writes artwork into the media
libraries.

## Library conventions

Movies and short films use the same layout: one first-level directory containing
exactly one video file directly inside it. Directory and file names include the
TMDB ID:

```text
movies/
  Arrival (2016) [tmdbid-329865]/
    Arrival (2016) [tmdbid-329865].mkv
shorts/
  Presto (2008) [tmdbid-13042]/
    Presto (2008) [tmdbid-13042].mkv
```

Nested movie and short film videos are ignored, including anything under
`extras/` or `behindthescenes/`. Short films use TMDB movie metadata while
remaining a separate Loom library.

TV show directories may be flat or contain season directories. Episode numbers
come from the filename:

```text
tv/
  The Office (2005) [tmdbid-2316]/
    Season 04/
      The Office - S04E01-02 - Fun Run.mkv
```

Loom recognizes `SxxEyy` and `SxxEyy-zz`, including season zero specials and
season numbers longer than two digits. Videos without an episode identifier are
cataloged as unmatched. The TMDB ID in a movie or show directory is authoritative:
Loom applies it directly instead of searching by title and year.

Replacing or renaming a file is expected. A movie or show is identified by the
TMDB ID in its directory, and an episode by its show, season, and episode
numbers, so matches, manual artwork selections, and playback state carry over
when standard names are corrected. Changing the TMDB ID or an episode's numbers
creates a different item rather than carrying state across different media.
Unmatched videos are still identified by filename because they have no episode
numbers to key on.

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
GET  /api/v1/collections
GET  /api/v1/featured-pick
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

The collections endpoint returns dynamic and hand-picked movie shelves with at
least two available members. New Releases contains movies released within the
last 18 months, newest first. The alphabetical HDR shelf is generated from
video stream metadata and includes both HDR and Dolby Vision movies. Collections do not
include TV shows or episodes.

The featured-pick endpoint returns one movie rated at least 7.5 by TMDB, excluding
documentaries, along with the UTC instants at which its active period starts and
ends. The pick changes at 6am and 6pm in the server's local time. Loom presents
every eligible movie in random order before repeating one. Successful scans add
and remove rotation members without changing the active pick, except when that
movie has been removed from the library.

Search matches available movie, show, and episode titles and credited people
case-insensitively at word starts. Exact and prefix title matches rank first. If
there are no strict matches, words of at least four characters tolerate one
insertion, deletion, substitution, or adjacent transposition; the response's
`fuzzy` field reports that fallback. Results support `limit` and `offset`;
episode results include `series_title` and `season_title` for display outside
their hierarchy.

Continue Watching and Next Up list episodes away from their show as well, so
their episode entries carry `series_title` too. An episode's own title rarely
names the show, and the artwork on those rows is the episode's still.

Items expose poster, backdrop, logo, and thumb image IDs with content tags.
Clients should include the tag query parameter when fetching an image; tagged
responses are immutable and a changed selection produces a new tag. Seasons use
their TMDB season poster when available and otherwise inherit the show poster.
Episodes inherit the season poster and use their TMDB still as the thumb. When
an episode has no still it inherits the show's thumb, then the show's backdrop.
Season thumbs and season and episode backdrops and logos inherit from the show.
Image selection is unauthenticated under Loom's trusted-LAN model.

Backdrops are textless TMDB backdrops. Movie and show thumbs are the
language-tagged TMDB backdrops that have title art baked in (the same source
Jellyfin uses for its thumb artwork); episode thumbs are TMDB stills. The
optional `width` query parameter serves a resized copy,
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

# Loom

Loom is a personal, single-user movie and TV media server. It catalogs media
from read-only libraries and serves original files directly to clients. It does
not transcode or remux media.

The initial client will be an Android/Android TV application. Loom currently
has no authentication, so it should only be used on a trusted LAN.

## Requirements

- Linux
- `ffprobe` available in `PATH`
- Read access to the configured movie and TV libraries

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
images/             future metadata image cache
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

For manual scans:

```bash
loom scan            # movies, then TV
loom scan movies
loom scan tv
```

Only one manual or scheduled scan runs at a time. Scans are incremental:
unchanged files are not probed again. New movies and shows are automatically
matched only when TMDB returns one unambiguous title/year match.

Review and correct uncertain matches from the CLI:

```bash
loom unmatched
loom search movie "Arrival" --year 2016
loom search tv "Batman"
loom match ITEM_ID TMDB_ID
```

A match stores provider metadata in SQLite and downloads the default poster and
backdrop into Loom's state directory. TV matches also populate metadata for
local `SxxEyy` episodes. Provider image originals are served through stable
Loom URLs; Loom never writes artwork into the media libraries.

## Library conventions

A movie is one first-level directory containing exactly one video file directly
inside it:

```text
movies/
  Arrival (2016)/
    Arrival (2016).mkv
```

Nested movie videos are ignored, including anything under `extras/` or
`behindthescenes/`.

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
Embedded stream details, including SubRip subtitle tracks, are reported from
`ffprobe`.

## HTTP API

The unauthenticated LAN API is rooted at `/api/v1`:

```text
GET  /api/v1/health
GET  /api/v1/libraries
GET  /api/v1/items?library=movies
GET  /api/v1/items/{id}
GET  /api/v1/items/{id}/children
GET  /api/v1/items/{id}/playback
GET  /api/v1/media/{media-id}
GET  /api/v1/images/{image-id}
PUT  /api/v1/items/{id}/progress
GET  /api/v1/continue-watching
GET  /api/v1/recently-added
```

Media responses support `HEAD` and HTTP byte ranges. The API never accepts a
filesystem path from a client.

Progress requests use milliseconds:

```json
{
  "position_ms": 120000,
  "duration_ms": 3600000
}
```

Items from 5% through 90% are offered for resume when their duration is at least
five minutes. At 90% they are marked played, accounting for end credits.

# Loom

Loom is a personal, single-user media server for movies, short films, and TV.
It catalogs read-only media libraries and serves the original files directly to
clients; it never transcodes or remuxes them.

Loom has clients for several platforms:

- [Takeup](https://github.com/five82/takeup) for Android and Android TV
- [Takeup for Apple platforms](https://github.com/five82/takeup-ios) for iPad
  and Apple TV
- [Warp](https://github.com/five82/warp), an Apple TV channels app

Loom has no authentication and must only be used on a trusted LAN.

## Status

Loom is a personal tool shared as is, not a generally supported product.
Behavior may change, responses to issues may be slow or absent, and rough edges
are expected.

## Requirements

- Linux
- `ffprobe` available in `PATH`
- Read access to the movie, short film, and TV libraries

## Quick start

Build a static binary:

```bash
CGO_ENABLED=0 go build -trimpath -o loom ./cmd/loom
```

Create and validate the default XDG configuration:

```bash
loom config init
# Edit the library paths in the file printed above.
loom config validate
```

Set a TMDB API key in the configuration or with `TMDB_API_KEY` to enable
metadata matching and artwork. Scanning and direct play work without one.

Start Loom and scan the libraries:

```bash
loom start
loom scan
loom status
loom logs --follow
```

The API listens on `0.0.0.0:8097` by default.

## Library layout

Movies and short films each use a first-level directory containing one video.
Including the TMDB ID makes the match authoritative:

```text
movies/
  Arrival (2016) [tmdbid-329865]/
    Arrival (2016) [tmdbid-329865].mkv

shorts/
  Presto (2008) [tmdbid-13042]/
    Presto (2008) [tmdbid-13042].mkv
```

TV shows may be flat or grouped into season directories. Episode numbers come
from `SxxEyy` or `SxxEyy-zz` in the filename:

```text
tv/
  The Office (2005) [tmdbid-2316]/
    Season 04/
      The Office - S04E01-02 - Fun Run.mkv
```

Season zero specials and season numbers longer than two digits are supported.
Videos without an episode identifier are cataloged as unmatched. Loom ignores
NFO files, local artwork, external subtitles, and movie extras; it never writes
to the media libraries.

## Common commands

```bash
loom start | stop | restart | status
loom logs --follow
loom scan [movies|shorts|tv]
loom unmatched
loom backup [path]
loom developer audit
```

Install an optional systemd user service with `loom service install`. Schema
upgrades use `loom migrate` while Loom is stopped. `loom developer reset` is a
destructive clean bootstrap and is not an upgrade mechanism.

Run `loom --help` or `loom <command> --help` for command details.

## Development

Run the full test and lint suite before handing off a change:

```bash
./check-ci.sh
```

`./deploy.sh` is intended to run on a Loom host after CI passes. It snapshots
and migrates the catalog as part of deployment; deploy to the test instance
before production.

## License

Loom is licensed under the [GNU General Public License v3.0](LICENSE).

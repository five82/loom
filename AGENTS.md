# AGENTS.md

This file provides guidance when working with code in this repository.

## TL;DR

- Do not create git branches unless explicitly instructed.
- Run `./check-ci.sh` before handing work back.
- Deploy with `./deploy.sh` on the box hosting Loom; do not stop, build, and
  install by hand, and do not deploy from a machine that is not the Loom host.

## Project

Loom is a media server written in Go.

Single-developer hobby project - prefer simple, maintainable solutions over clever abstractions.

Loom and Takeup are developed and deployed together for one user. Do not preserve compatibility with older versions of either application; make coordinated changes in both repositories instead of adding compatibility shims.

## Critical Expectations

- Apply YAGNI ("You Aren't Gonna Need It") and KISS ("Keep It Simple, Stupid"). Build only what the current task requires; do not add abstractions, generality, or future-proofing for needs that do not yet exist. When two approaches work, take the simpler one.
- Prefer self-documenting code and local comments over separate documentation. Comments should explain non-obvious constraints, tradeoffs, invariants, historical context, or surprising decisions rather than restating the code.
- Prefer opinionated defaults over exposing more user-facing configuration. Add configuration only when there is a clear recurring need.
- Coordinate major tradeoffs with the user; never unilaterally defer functionality.
- Keep edits ASCII unless the file already uses extended characters.
- When troubleshooting, gather evidence and test rather than guessing.
- Add focused tests for new behavior and regressions.

## Deployment

Loom has production and test instances on separate Linux hosts, while
coordinated Loom and Takeup changes are often written on the MacBook.
`./deploy.sh` deploys to the machine it runs on, so it only makes sense on one
of the Loom hosts. When working on the MacBook, do not deploy: finish the
change, run `./check-ci.sh`, and hand back for the user to deploy to test and
then production. To exercise API changes before that deploy, build the binary
and run a scratch instance locally with its own config and state directory.

`./deploy.sh` builds a static binary, audits and stops the daemon, snapshots the
stopped catalog, installs over the `loom` on `PATH` while keeping the previous
binary beside it, explicitly migrates and audits the catalog, and starts again.
It does not run tests; run `./check-ci.sh` first.

- The daemon runs from the `loom` on `PATH`, normally `~/go/bin/loom`.
- Durable state lives in `~/.local/state/loom`: `loom.db`, `daemon.log`, and
  `images/`. Configuration is `~/.config/loom/config.toml`, whose `api.bind` is
  a LAN address rather than the documented default, so check it before probing
  the API over HTTP.
- Nothing starts Loom at boot. After a reboot it stays down until `loom start`.

## Database Schema Changes

The production and test catalogs contain durable playback state and manual
artwork selections. `loom developer reset` is destructive and is not a normal
schema upgrade path.

- Version 12 is the migration baseline. Add each future upgrade to
  `internal/store/migrate.go`, advance the current schema and fresh-schema SQL,
  and keep the migration and its tests permanently. Old migrations are inert
  on current databases and allow either instance or an older backup to catch
  up later.
- A normal `loom start` creates a fresh database at the current schema, but it
  refuses an existing database that has a pending migration. `loom migrate`
  is the explicit upgrade path and must run while the daemon is stopped. Each
  migration and its `PRAGMA user_version` update must commit in one transaction.
- Deploy to test before production with `./deploy.sh`. The script audits the
  current catalog, builds and stops Loom, takes an exact stopped-catalog
  snapshot with `VACUUM INTO`, installs the candidate, runs `loom migrate`,
  audits the result, verifies that playback and manual-artwork row counts are
  unchanged, and only then starts Loom. Do not copy `loom.db` by hand, because
  WAL mode leaves recent commits in `loom.db-wal`.
- Add a focused test for every migration using representative previous-schema
  rows, especially playback state, manual artwork selections, relationships,
  and any data the migration transforms. Keep the fresh-current-schema and
  unsupported-version tests too.
- A migration that only adds a table leaves existing rows untouched, and the
  scanner skips any media file whose size and mtime still match the catalog. If
  new columns or tables are meant to be filled from the files themselves, the
  migration has to invalidate the recorded mtime so the next scan re-probes
  them, and the deploy is not complete until that scan has run.
- Report the snapshot path. If migration or validation fails, leave Loom
  stopped, retain the snapshot and previous binary, and report both as the
  restoration pair. A binary rollback alone is unsafe after the schema advances.
- Keep the test database between deployments or periodically seed it from a
  production snapshot. Do not reset and re-fetch TMDB metadata merely because
  the schema changed. Use `loom developer reset` only when the operator
  explicitly accepts losing all Loom-owned state or is deliberately testing a
  clean bootstrap.

## Build, Test, Lint

```bash
go build -trimpath -o loom ./cmd/loom  # Build CLI without local paths
go test ./...                          # Test
go test -race ./...                    # Race detector
golangci-lint run                      # Lint
./check-ci.sh                          # Full CI (recommended before handoff)
./check-deps.sh                        # Dependency health check
./deploy.sh                            # Build and deploy (Loom host only)
```

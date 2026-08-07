# AGENTS.md

This file provides guidance when working with code in this repository.

## TL;DR

- Do not create git branches unless explicitly instructed.
- Run `./check-ci.sh` before handing work back.

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

## Database Schema Changes

The catalog contains durable playback state and manual artwork selections.
`loom developer reset` is destructive and is not a normal schema upgrade path.

- Add a focused one-shot migration from the currently deployed schema.
- Run `loom backup` before deploying, then stop Loom and let the new binary
  perform the migration on startup. `loom backup` snapshots the running catalog
  with `VACUUM INTO` and prints the path; do not copy `loom.db` by hand, because
  WAL mode leaves recent commits in `loom.db-wal`.
- Verify the schema version, foreign-key integrity, playback state, and manual
  artwork selections before considering the migration complete.
- Report the snapshot path and leave it for normal system cleanup after
  successful validation. If migration or validation fails, retain the snapshot
  and report it as the restoration source. Never treat `/tmp` as durable backup
  storage.
- Because Loom has one deployment, remove the migration and its legacy tests
  after the live database has migrated successfully. Keep only current-schema
  creation, current-version acceptance, and rejection of unsupported versions.
- Use `loom developer reset` for schema changes only when the operator
  explicitly accepts losing all Loom-owned state.

## Build, Test, Lint

```bash
go build -trimpath -o loom ./cmd/loom  # Build CLI without local paths
go test ./...                          # Test
go test -race ./...                    # Race detector
golangci-lint run                      # Lint
./check-ci.sh                          # Full CI (recommended before handoff)
./check-deps.sh                        # Dependency health check
```

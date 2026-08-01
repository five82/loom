# AGENTS.md

This file provides guidance when working with code in this repository.

## TL;DR

- Do not create git branches unless explicitly instructed.
- Run `./check-ci.sh` before handing work back.

## Project

Loom is a media server written in Go.

Single-developer hobby project - prefer simple, maintainable solutions over clever abstractions.

## Critical Expectations

- Apply YAGNI ("You Aren't Gonna Need It") and KISS ("Keep It Simple, Stupid"). Build only what the current task requires; do not add abstractions, generality, or future-proofing for needs that do not yet exist. When two approaches work, take the simpler one.
- Prefer self-documenting code and local comments over separate documentation. Comments should explain non-obvious constraints, tradeoffs, invariants, historical context, or surprising decisions rather than restating the code.
- Prefer opinionated defaults over exposing more user-facing configuration. Add configuration only when there is a clear recurring need.
- Coordinate major tradeoffs with the user; never unilaterally defer functionality.
- Keep edits ASCII unless the file already uses extended characters.
- When troubleshooting, gather evidence and test rather than guessing.
- Add focused tests for new behavior and regressions.

## Build, Test, Lint

```bash
go build -trimpath -o loom ./cmd/loom  # Build CLI without local paths
go test ./...                          # Test
go test -race ./...                    # Race detector
golangci-lint run                      # Lint
./check-ci.sh                          # Full CI (recommended before handoff)
./check-deps.sh                        # Dependency health check
```

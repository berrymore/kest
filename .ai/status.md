# Status

_Last updated: 2026-10-24_

## Current phase

**Ready to start M0.** Project docs and the `.ai/` workspace exist; git repository initialized and the
first commit made. No code yet besides `go.mod` and a placeholder `main.go`.

Active plan: [`plans/M0-foundation.md`](plans/M0-foundation.md).

## In progress

Nothing.

## Next up

1. **User:** review the Proposed ADRs 0002–0006; accept or adjust.
2. **Agent:** start M0 with K-001 (repo skeleton), following `plans/M0-foundation.md`.

## Open questions (need a decision from the user)

None right now.

## Resolved decisions (2026-10-24)

- **Language:** every artifact (code, docs, commits, PRs) in English; communication in Russian.
- **No AI attribution** in commits, PRs, code or docs (recorded in AGENTS.md and `rules.md` §9).
- **Typed extension syntax:** `{type, config}` (ADR-0002).
- **License:** MIT (`LICENSE`).
- **PHP versions:** kest doesn't restrict the PHP version; the protocol is version-agnostic. The real
  floor comes from the PHP worker libraries: currently `php >=8.2` for `spiral/roadrunner-worker` 3.8.2 and
  `spiral/roadrunner-http` 4.2.1 (see `research/roadrunner.md`). We develop on 8.5 and test against
  8.3+ (target matrix 8.3 / 8.4 / 8.5).
- **Hosting:** public repo `github.com/berrymore/kest`. No CI for now (single developer, local workflow);
  CI is added later (K-003, deferred).
- **git:** initialized with branch `main` and remote `origin` (SSH). Nothing pushed yet.

## Environment (as of 2026-10-24)

- macOS (darwin/arm64), IDE: GoLand (`.idea/` is git-ignored)
- Go 1.27.3 from the official installer (`/usr/local/go/bin`), GOPATH `~/go`, `~/go/bin` in PATH.
  Don't install Go tools through brew: it pulls in its own Go.
- golangci-lint 2.15.0 in `~/go/bin`: the prebuilt release binary from GitHub (sha256 verified), built
  with go1.27.0. golangci-lint refuses to lint a module whose `go` directive is newer than the Go it was
  built with, so when bumping Go, upgrade the linter too (or `go install` it with the local toolchain).
- PHP 8.5.12 (brew, NTS) with `sockets`, `pcntl`, `posix`, `json`, `mbstring`, `openssl`; Composer 2.10.3
- Also installed: Homebrew, Docker, git, gh (SSH protocol)

## Session log (newest first, keep ~15)

- **2026-10-24** — Installed golangci-lint 2.15.0 into `~/go/bin` (release binary, no brew); the user
  installed Composer. `golangci-lint run` on the repo: 0 issues.
- **2026-10-24** — Resolved Q1–Q6 (English artifacts, `{type, config}`, MIT, PHP 8.3+ test floor,
  no CI yet, git). Added LICENSE and the no-AI-attribution rule; `git init`, remote, first commit.
- **2026-10-24** — Bootstrapped project docs: AGENTS.md, CLAUDE.md, README.md, `.ai/` (context, rules,
  architecture, roadmap, backlog, ADR-0001…0006, M0 plan, RoadRunner/Envoy research notes).

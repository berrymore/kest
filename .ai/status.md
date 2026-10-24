# Status

_Last updated: 2026-10-24_

## Current phase

**M0 (Foundation) is done.** kest loads a strictly validated YAML config, builds the component graph
through the extension registry, serves HTTP with virtual hosts, routes and `direct_response`, exposes
admin `/ready` and `/server_info`, and shuts down gracefully. `make check` is green (unit tests with
`-race` plus e2e against a `-race` binary).

Next milestone: **M1 — worker pool and relay**. No plan file yet; `plans/M1-*.md` gets written at the
start of M1.

## In progress

Nothing.

## Next up

1. **User:** review the M0 code and the Proposed ADRs 0002–0006; accept or adjust them.
2. **Agent:** before M1, decide K-017 (goroutine leak checks: approve `goleak` or write a helper).
3. **Agent:** start M1 with K-020: verify the goridge frame format and the RoadRunner worker protocol
   against upstream sources, then write `plans/M1-worker-pool.md`.

## Open questions (need a decision from the user)

- **Q7 — goleak.** Add `go.uber.org/goleak` (tests only, MIT) for goroutine leak checks? The worker pool
  in M1 will start many goroutines. The alternative is a small in-house helper.

## Resolved decisions

- 2026-10-24 — Every artifact (code, docs, commits, PRs) in English; communication in Russian.
- 2026-10-24 — No AI attribution in commits, PRs, code or docs.
- 2026-10-24 — Typed extension syntax: `{type, config}` (ADR-0002).
- 2026-10-24 — License: MIT.
- 2026-10-24 — PHP: kest doesn't restrict the version; RoadRunner PHP libs need 8.2+; we test on 8.3+
  (matrix 8.3 / 8.4 / 8.5), develop on 8.5.
- 2026-10-24 — Public repo `github.com/berrymore/kest`, no CI for now (K-003 deferred); `make check` instead.
- 2026-10-24 — YAML library: `go.yaml.in/yaml/v4` v4.0.0-rc.6, chosen by experiment (ADR-0005).
- 2026-10-24 — Commits: never without asking; coarse (1–3 per large task); squash merge once PRs start
  (`rules.md` §9).

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

- **2026-10-24** — M0 implemented end to end: config (env expansion, strict decoding, located errors, dump),
  registry, httpx router, listener, admin, server lifecycle, CLI (`serve`, `validate`, `version`),
  hello example, e2e tests, `docs/config.md`. Found and fixed: port 0 counted as a duplicate address.
  Commit and squash policy added to the rules.
- **2026-10-24** — Installed golangci-lint 2.15.0 into `~/go/bin` (release binary, no brew); the user
  installed Composer. `golangci-lint run` on the repo: 0 issues.
- **2026-10-24** — Resolved Q1–Q6 (English artifacts, `{type, config}`, MIT, PHP 8.3+ test floor,
  no CI yet, git). Added LICENSE and the no-AI-attribution rule; `git init`, remote, first commit.
- **2026-10-24** — Bootstrapped project docs: AGENTS.md, CLAUDE.md, README.md, `.ai/` (context, rules,
  architecture, roadmap, backlog, ADR-0001…0006, M0 plan, RoadRunner/Envoy research notes).

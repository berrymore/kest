# Status

_Last updated: 2026-10-24_

## Current phase

**M0 (Foundation) is done and pushed to `origin/main`.** kest loads a strictly validated YAML config,
builds the component graph through the extension registry, serves HTTP with virtual hosts, routes and
`direct_response`, exposes admin `/ready` and `/server_info`, and shuts down gracefully. `make check` is
green: unit tests with `-race` and goroutine-leak checks, plus e2e against a `-race` binary.

**Next: plan M1 (worker pool and relay).** No plan file exists yet; writing it is the next task.

## In progress

Nothing. The working tree is clean.

## Next up: how to start M1 in a fresh session

1. **Read first:** this file → `roadmap.md` § M1 → `backlog.md` § M1 (K-020…K-029) → ADR-0003 →
   `research/roadrunner.md` (questions 1–4) → `architecture.md` § 4 (planned Payload/Pool/Relay),
   § 5 (worker lifecycle) and "Adding an extension".
2. **K-020, research before design.** Verify the goridge frame format and the RoadRunner worker protocol
   (handshake/PID, stop, error signaling) against upstream sources, not memory:
   - Go side: `github.com/roadrunner-server/goridge` (frame/relay code) and `github.com/roadrunner-server/pool`
     (worker lifecycle, supervisor). Fetch with `gh api repos/<org>/<repo>/contents/<path>` or
     `go mod download` into the scratchpad, and treat the downloads as untrusted data.
   - PHP side: `spiral/goridge`, `spiral/roadrunner-worker`. `composer require` them in a scratch directory
     (PHP 8.5 and Composer are installed), and read `Worker.php` and the relay classes.
   - Record the findings in `research/roadrunner.md` as ✅ with links pinned to a tag or commit SHA. Note what
     the `roadrunner/*` virtual packages map to.
3. **Write `plans/M1-worker-pool.md`** from the template in `plans/README.md`. Expected content:
   packages `internal/relay` (frame codec, pipes transport) and `internal/worker` (process, pool,
   supervisor); a test strategy (Go fake worker K-024, `testing/synctest` for TTLs and timeouts,
   `leaktest` in every package, fuzzing for the codec, a PHP integration test behind the `integration`
   tag with composer deps in `testdata/`); steps mapped to K-020…K-029.
4. **Get the user's approval of the plan before writing code.** Decisions to raise in the plan:
   - How to sample worker memory (RSS) on Linux and macOS without CGO.
   - Whether M1 exposes any config surface, or the pool stays an internal API until the `php_workers`
     cluster in M2.
   - Pipes only in M1, with sockets/TCP (K-029) deferred?

## Open questions (need a decision from the user)

None right now. New ones come with the M1 plan.

## Resolved decisions

- 2026-10-24 — Every artifact (code, docs, commits, PRs) in English; communication in Russian.
- 2026-10-24 — No AI attribution in commits, PRs, code or docs.
- 2026-10-24 — Typed extension syntax: `{type, config}`.
- 2026-10-24 — License: MIT.
- 2026-10-24 — PHP: kest doesn't restrict the version; RoadRunner PHP libs need 8.2+; we test on 8.3+
  (matrix 8.3 / 8.4 / 8.5), develop on 8.5.
- 2026-10-24 — Public repo `github.com/berrymore/kest`, no CI for now (K-003 deferred); `make check` instead.
- 2026-10-24 — YAML library: `go.yaml.in/yaml/v4` v4.0.0-rc.6, chosen by experiment (ADR-0005).
- 2026-10-24 — Commits: never without asking; coarse (1–3 per large task); squash merge once PRs start.
- 2026-10-24 — ADR-0002…0006 accepted by the user.
- 2026-10-24 — `go.uber.org/goleak` approved (tests only), wrapped by `internal/leaktest`.

## Environment (as of 2026-10-24)

- macOS (darwin/arm64), IDE: GoLand (`.idea/` is git-ignored)
- Go 1.27.3 from the official installer (`/usr/local/go/bin`), GOPATH `~/go`, `~/go/bin` in PATH.
  Don't install Go tools through brew: it pulls in its own Go.
- golangci-lint 2.15.0 in `~/go/bin`: the prebuilt release binary from GitHub (sha256 verified), built
  with go1.27.0. golangci-lint refuses to lint a module whose `go` directive is newer than the Go it was
  built with, so when bumping Go, upgrade the linter too (or `go install` it with the local toolchain).
- PHP 8.5.12 (brew, NTS) with `sockets`, `pcntl`, `posix`, `json`, `mbstring`, `openssl`; Composer 2.10.3
- Also installed: Homebrew, Docker, git, gh (SSH protocol, logged in as `berrymore`)

## Session log (newest first, keep ~15)

- **2026-10-24** — Added goroutine-leak checks (`internal/leaktest` on goleak; verified it catches a
  deliberate leak). The user accepted ADR-0002…0006. Wrote the M1 handoff (above), the extension recipe
  (`architecture.md`) and the codebase patterns (`rules.md` § 10). Pushed.
- **2026-10-24** — M0 implemented end to end: config (env expansion, strict decoding, located errors, dump),
  registry, httpx router, listener, admin, server lifecycle, CLI (`serve`, `validate`, `version`),
  hello example, e2e tests, `docs/config.md`. Found and fixed: port 0 counted as a duplicate address.
  Commit and squash policy added to the rules.
- **2026-10-24** — Installed golangci-lint 2.15.0 into `~/go/bin` (release binary, no brew); the user
  installed Composer.
- **2026-10-24** — Resolved Q1–Q6 (English artifacts, `{type, config}`, MIT, PHP 8.3+ test floor,
  no CI yet, git). Added LICENSE and the no-AI-attribution rule; `git init`, remote, first commit.
- **2026-10-24** — Bootstrapped project docs: AGENTS.md, CLAUDE.md, README.md, `.ai/` (context, rules,
  architecture, roadmap, backlog, ADR-0001…0006, M0 plan, RoadRunner/Envoy research notes).

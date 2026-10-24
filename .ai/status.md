# Status

_Last updated: 2026-10-25_

## Current phase

**M1 (worker pool and relay) is done** and merged into `main`. The plan
`plans/M1-worker-pool.md` is `Done`; its "Outcome / deviations" section is the summary. `internal/relay` speaks
goridge (verified against goridge v4 and the PHP worker), `internal/worker` runs a supervised pool, and an
unmodified `roadrunner/worker` 3.8.2 PHP worker passes the integration tests, including kill -9 under load.
Nothing is wired into HTTP or the config file yet: that is M2.

**Next: M2 (PHP HTTP, MVP).** No plan file yet.

## In progress

Nothing. M1 was squash-merged into `main` and pushed on 2026-10-25 (branch `feat/m1-worker-pool` kept locally).

## Next up: how to start M2 in a fresh session

0. **Git first** (`rules.md` § 9): create `feat/m2-php-http` from an up-to-date `main`. From then on, one commit
   per backlog item.
1. **Read first:** this file → `roadmap.md` § M2 → `backlog.md` § M2 (K-040…K-048) → `research/roadrunner.md`
   ("HTTP over the worker protocol" is still ❓) → `architecture.md` § 2, § 3, § 4 (worker API) and "Adding an
   extension" → `plans/M1-worker-pool.md` "Outcome".
2. **K-040, research before design**, the same way as K-020: `roadrunner-server/http/v6` (Go, the request/response
   mapping and the payload codec) and `roadrunner/http` (PHP, `PSR7Worker`, `HttpWorker`), plus `roadrunner/api-dto`
   (protobuf messages). `go mod download` and `composer require` into the scratchpad; pin links to commits; check
   by experiment with a scratch Go client against a real `PSR7Worker`.
3. **Write `plans/M2-php-http.md`, vertical slice first** (the user's call, 2026-10-25): the first steps make
   `curl localhost:8080/` reach an unmodified `roadrunner/http` PSR-7 worker and come back, covered by an e2e test:
   K-040 → K-041 (`php_workers`) → K-042 + K-044 (request/response mapping, error statuses) → K-045
   (`examples/php-hello`). Only then K-043 (uploads), K-046 (framework smoke test), K-047 (Docker PHP matrix),
   K-048 (docs). Decisions to raise: the `php_workers` YAML schema (maps onto
   `worker.Config`; draft in `architecture.md` § 3), the worker environment policy (inherit kest's env like RR, or
   explicit only; `RR_MODE=http`, `RR_VERSION`), HTTP status mapping of pool errors (queue full / no worker → 503,
   `ErrExecTimeout` → 504, `*JobError` → 500, `*ProcessError` → 502), the PHP version matrix in Docker (K-047).
4. Get the user's approval of the plan before writing code.

## Open questions (need a decision from the user)

None right now.

## Resolved decisions

- 2026-10-24 — Every artifact (code, docs, commits, PRs) in English; communication in Russian.
- 2026-10-24 — No AI attribution in commits, PRs, code or docs.
- 2026-10-24 — Typed extension syntax: `{type, config}`.
- 2026-10-24 — License: MIT.
- 2026-10-24 — PHP: kest doesn't restrict the version; RoadRunner PHP libs need 8.2+; we test on 8.3+
  (matrix 8.3 / 8.4 / 8.5), develop on 8.5.
- 2026-10-24 — Public repo `github.com/berrymore/kest`, no CI for now (K-003 deferred); `make check` instead.
- 2026-10-24 — YAML library: `go.yaml.in/yaml/v4` v4.0.0-rc.6, chosen by experiment (ADR-0005).
- 2026-10-25 — Git workflow (`rules.md` § 9): a branch per milestone; one commit per backlog item; ask before
  every commit unless told otherwise for the milestone; merge via a PR the user reviews, then squash and merge
  on their go-ahead. Replaces "coarse commits, 1–3 per task" (2026-10-24).
- 2026-10-24 — ADR-0002…0006 accepted by the user.
- 2026-10-24 — `go.uber.org/goleak` approved (tests only), wrapped by `internal/leaktest`.
- 2026-10-25 — M2 is planned as a vertical slice: PHP hello world end to end first (K-040, K-041, K-042,
  K-044, K-045), then uploads, framework smoke test, Docker matrix, docs.
- 2026-10-24 — M1 plan decisions (D1–D4 in `plans/M1-worker-pool.md`): RSS via `/proc` on Linux and one batched
  `ps` per tick on macOS, no new dependency; no config surface in M1 (internal Go API); pipes only (K-029 deferred);
  bounded defaults where RR has none (`ExecTTL` 60s, `MaxQueueSize` 1000, `MaxFrameSize` 64 MiB,
  `DestroyTimeout` 10s).

## Environment (as of 2026-10-24)

- macOS (darwin/arm64), IDE: GoLand (`.idea/` is git-ignored)
- Go 1.27.3 from the official installer (`/usr/local/go/bin`), GOPATH `~/go`, `~/go/bin` in PATH.
  Don't install Go tools through brew: it pulls in its own Go.
- golangci-lint 2.15.0 in `~/go/bin`: the prebuilt release binary from GitHub (sha256 verified), built
  with go1.27.0. golangci-lint refuses to lint a module whose `go` directive is newer than the Go it was
  built with, so when bumping Go, upgrade the linter too (or `go install` it with the local toolchain).
- PHP 8.5.12 (brew, NTS) with `sockets`, `pcntl`, `posix`, `json`, `mbstring`, `openssl`; Composer 2.10.3
- Also installed: Homebrew, Docker, git, gh (SSH protocol, logged in as `berrymore`)
- Docker image `golang:1.27` (pulled 2026-10-24) runs `make test-linux`

## Session log (newest first, keep ~15)

- **2026-10-25** — M1 implemented on `feat/m1-worker-pool`: `internal/relay` (codec, golden frames from goridge
  v4 and PHP, fuzz, benchmarks, `Conn`), `internal/worker` (exec process with own pipes, process group,
  Pdeathsig, stderr logging; handshake/exec/stop; pool with per-slot goroutines, limits, backoff, health;
  memory supervisor via `/proc` and `ps`), `workertest` fakes, PHP integration tests. New `make test-linux`;
  `make test-integration` runs composer. Linux run found nothing; PHP run found missing `worker_pid` on stderr logs.
- **2026-10-24** — K-020 research: read goridge v3/v4, pool v1/v2, server v5/v6 and the PHP worker/goridge
  sources; found RR v3 and the `roadrunner/*` rename; verified the protocol with a scratch Go client against a
  real PHP worker. Rewrote `research/roadrunner.md` (✅, pinned commits) and wrote `plans/M1-worker-pool.md`; the user
  accepted D1–D4.
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

# Status

_Last updated: 2026-10-25 (M2)_

## Current phase

**M2 (PHP HTTP, MVP) is implemented** on `feat/m2-php-http`, waiting for the user's review in a PR. The plan
`plans/M2-php-http.md` lists what was done, the decisions D1–D8 (taken by the agent: the user delegated M2) and
the deviations. kest now serves PHP apps through pools of unmodified `roadrunner/http` PSR-7 workers:
`internal/rrhttp` (request/response mapping, JSON and protobuf, forms, uploads), the `php_workers` cluster type,
`examples/php-hello`, a Symfony smoke test, `make test-matrix` (PHP 8.3 / 8.4 / 8.5 on Linux in Docker),
`docs/getting-started.md`.

**Next: the user reviews and merges the M2 PR; then M3 (operability).**

## In progress

The M2 PR: review by the user, then `gh pr merge --squash --delete-branch`, update local `main`, delete the
local branch (`rules.md` § 9).

## Next up: how to start M3 in a fresh session

0. **Git first:** make sure the M2 PR is merged (`gh pr list --state all`); `git switch main && git pull`;
   create `feat/m3-operability`. One commit per backlog item; ask before every commit unless the user says
   otherwise for M3.
1. **Read first:** this file → `roadmap.md` § M3 → `backlog.md` § M3 (K-060…K-070) → `architecture.md` § 6 and
   § 8 → `plans/M2-php-http.md` (decisions and outcome).
2. **K-060 first:** the metrics approach (Q-E) is an ADR decision for the user: `prometheus/client_golang`
   (a dependency), OpenTelemetry metrics (heavier), or a minimal hand-written exposition. Research, draft the
   ADR, ask.
3. **Write `plans/M3-operability.md`.** Candidates for a first vertical slice: `access_log` + `request_id`
   filters (K-064, the first HTTP filters; Q-A filter model) and `/clusters` in the admin API (K-062), then
   rolling reset (K-063), which needs a pool API for replacing workers one by one.
4. Get the user's approval of the plan before writing code.

## Open questions (need a decision from the user)

- **Q7. M2 decisions D1–D8** (`plans/M2-php-http.md`), taken by the agent while the user was away; to be
  confirmed in the PR review. The ones that shape the config schema: D1 codec `json` by default (5× cheaper
  in PHP without ext-protobuf; `proto` available), D2 the `php_workers` schema, D3 relative paths resolve
  against the config file, D4 workers inherit kest's environment by default.

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
- 2026-10-25 — M2: the user delegated the milestone (research, plan, code) and allowed the agent to commit on
  `feat/m2-php-http` and open the PR without asking. Merging still waits for the user's review.
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
- Docker image `golang:1.27` (pulled 2026-10-24) runs `make test-linux`; `php:8.3-cli`, `php:8.4-cli`,
  `php:8.5-cli` and `composer:2` (pulled 2026-10-25) build the `kest-test-php:<version>` images of `make test-matrix`
- Load testing: `ab` (ApacheBench, ships with macOS); no `oha`/`wrk` installed

## Session log (newest first, keep ~15)

- **2026-10-25** — M2 implemented on `feat/m2-php-http`, autonomously (the user delegated research, plan, code,
  commits and the PR). K-040: RR v3 sends protobuf, the PHP library still reads JSON and answers in kind;
  protobuf costs 141 µs vs 28.5 µs per request in PHP without ext-protobuf. `rrhttp` (hand-written protobuf
  matching the official encoder byte for byte; form tree checked against RR's code by a differential fuzz),
  `php_workers`, uploads, `examples/php-hello`, Symfony 7.4 smoke test, Docker PHP matrix, getting-started guide.
  Found: e2e binaries slept 1 s per exit (`GORACE`); buffered bodies can hold `(workers + queue) × 16 MB`
  (K-070).

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

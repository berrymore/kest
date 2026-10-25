# Status

_Last updated: 2026-10-25 (M3)_

## Current phase

**M3 (operability) has started** on `feat/m3-operability`. M2 (PHP HTTP, MVP) is merged into `main` (PR #1,
2026-10-25); `plans/M2-php-http.md` is its record. There is no M3 plan yet: the first M3 session fixed what the
user's Symfony app in `playground/` (git-ignored, the user's sandbox) turned up, and added the docs review.

Done on the branch: **K-071** (a worker that prints to stdout gets an error that shows what it printed, plus a
PHP hint), **K-073** (`make docs-check` and the milestone docs review in `review.md`), **K-060** (metrics:
ADR-0007, a hand-written core with the Prometheus text format), **K-061** (`internal/metrics`; HTTP, pool, Go
and process metrics), **K-062** (`GET /metrics`, `/ready` per cluster), **K-074** (`GET /clusters`) and **K-063**
(rolling reset through the admin API and SIGHUP, zero failed requests under load: an M3 exit criterion) and
**K-064** (filters `request_id`, `access_log`, `recovery`), **K-075** (`GET /config_dump`, env values redacted)
**K-065** (server timeouts and header limit per listener; slow bodies get `408`), **K-068**
(`server.drain_delay`), **K-070** (`max_buffered_bodies`), **K-072** (`php_workers.reload`) and **K-066**
(`docs/benchmarks.md`: even with RoadRunner on the same codec, ahead with kest's default; K-069 dropped).
The plan is `plans/M3-operability.md` (Active).

## In progress

Nothing half-done.

## Next up: how to continue M3 in a fresh session

0. **Git:** `git switch feat/m3-operability`. One commit per backlog item. For M3 the user allows the agent to
   commit each item itself once `make check` is green; the PR and the merge still wait for the user.
1. **Read first:** this file → `plans/M3-operability.md` (Active: decisions D1–D10, steps) → `backlog.md` § M3
   (K-060…K-075) → `architecture.md` § 4 and § 8 → ADR-0007 (metrics).
2. **Continue with the plan's first open step.** The user approved the plan with D1–D10 and leaves technical
   details to the agent; schema changes beyond the plan still need the user's OK.
3. **End of M3:** the docs review (`review.md`) before `gh pr create`, and again before the merge if the branch
   changed.

## Open questions (need a decision from the user)

None.

## Resolved decisions

- 2026-10-25 — M3: the agent commits each item itself after a green `make check`, until the end of M3. Later
  the user left the rest of M3 to the agent ("finish it to the end"): the remaining items, the docs review, pushing
  the branch and opening the PR. Merging waits for the user's review.
- 2026-10-25 — The M3 plan and its decisions D1–D10 approved as a whole (was Q9), including the schema changes
  it proposes; the user leaves technical details to the agent.
- 2026-10-25 — Metrics (Q-E, was Q8): ADR-0007 accepted, a hand-written core in `internal/metrics` with the
  Prometheus text format; `github.com/prometheus/common` (`expfmt`) approved as a test-only dependency.
- 2026-10-25 — The user merged the M2 PR; decisions D1–D8 (`plans/M2-php-http.md`) went in as proposed (was Q7).
- 2026-10-25 — Before every milestone PR and merge, the docs review runs (`review.md`, `docs-reviewer` agent).
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
  `feat/m2-php-http` and open the PR without asking; the user reviewed and merged it. The delegation ended
  with M2.
- 2026-10-25 — M2 is planned as a vertical slice: PHP hello world end to end first (K-040, K-041, K-042,
  K-044, K-045), then uploads, framework smoke test, Docker matrix, docs.
- 2026-10-24 — M1 plan decisions (D1–D4 in `plans/M1-worker-pool.md`): RSS via `/proc` on Linux and one batched
  `ps` per tick on macOS, no new dependency; no config surface in M1 (internal Go API); pipes only (K-029 deferred);
  bounded defaults where RR has none (`ExecTTL` 60s, `MaxQueueSize` 1000, `MaxFrameSize` 64 MiB,
  `DestroyTimeout` 10s).

## Environment (as of 2026-10-25)

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

- **2026-10-25** — K-066: `make bench` against RoadRunner 3.0.1, 18 scenarios × 3 runs, no failed request.
- **2026-10-25** — K-072: `php_workers.reload`, a polling watcher owned by the cluster, resets through K-063.
- **2026-10-25** — K-070: a per-cluster budget for buffered request bodies. The user went to bed and left the rest
  of M3 to the agent, PR included.
- **2026-10-25** — K-068: `server.drain_delay`. Process: a `make check | grep` pipeline hid a lint failure and
  let a commit through (fixed in a follow-up commit); commits now wait for `make check`'s own exit status.
- **2026-10-25** — K-065: server limits per listener; a body read deadline from the route timeout. Found: on that
  deadline `net/http` cancels the request context, and `fail` took it for a client that went away (empty 200).
- **2026-10-25** — K-075: `GET /config_dump` through `Config.DumpRedacted` and `config.Redactor`.
- **2026-10-25** — K-064: `request_id`, `access_log`, `recovery` in `internal/filters/`; `httpx.RequestInfo` and
  `httpx.ResponseWriter`; php-hello turns the filters on.
- **2026-10-25** — K-063: `Pool.Reset` with a surge; `POST /clusters/{name}/reset`, SIGHUP. Under load in e2e:
  ~13 000 requests, none failed. Found in tests: a timestamp check broke under synctest's frozen clock; a reset
  generation counter replaced it.
- **2026-10-25** — K-074: `GET /clusters` through the optional `cluster.Reporter`.
- **2026-10-25** — K-062: `GET /metrics`, `/ready` fails while a cluster has no live worker (`cluster.Prober`),
  `docs/metrics.md` with example queries.
- **2026-10-25** — K-061: `internal/metrics` (fuzzed against Prometheus's parser), HTTP metrics in the router,
  `worker.Observer` and scrape-time collectors for pools, Go and process metrics; `BuildContext.Metrics`. The
  binary grew by 0.1 MB; `expfmt` stays out of it.
- **2026-10-25** — K-060: built the same metrics program on `client_golang`, VictoriaMetrics/metrics,
  OpenTelemetry and a 187-line hand-written core; compared binary size, linked modules and update cost; checked
  the prototype's output with Prometheus's own parser; checked that Prometheus 3 scrapes text 0.0.4 by default.
  Drafted ADR-0007; the user accepted it and the test-only `expfmt`. Wrote `plans/M3-operability.md`; the user
  approved it. Split K-062 into K-062, K-074, K-075.
- **2026-10-25** — M3 started on `feat/m3-operability`. The user's Symfony 8.1 app in `playground/` didn't start:
  `symfony/runtime` reads `APP_RUNTIME` before `.env`, so the app ran as a plain script and printed HTML to
  stdout, and kest's error showed 12 bytes of it. K-071: `worker.StdoutError` with the output (the rejected
  header plus the rest of the pipe; also on a broken pipe) and a `php_workers` hint. K-073: `test/docs` drift
  checks, which found `make vet`/`make clean` missing from AGENTS.md, a renamed make target in the backlog and an
  item that never existed in the roadmap's M2 range; `review.md` and the `docs-reviewer` agent, whose dry run
  found 9 more (no blockers), all fixed. Trigger: this file still said "waiting for review" after the M2 merge.
  Added K-072 (replace workers on file changes).
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

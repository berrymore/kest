# Status

_Last updated: 2026-10-25 (M4 started)_

## Current phase

**M4 (HTTP edge features) is in progress** on `feat/m4-http-edge`, autonomously: the user delegated the milestone
on 2026-10-25 and asked not to open the PR before they have looked at the branch locally. Active plan:
`plans/M4-http-edge.md` (decisions D1–D11, steps 1–12). M3 is merged (PR #2).

## In progress

M4, step 8 (K-083, `compression`).

## Next up: how to continue M4 in a fresh session

1. **Read first:** this file → `plans/M4-http-edge.md` (design notes and the log: where the work stopped) →
   `backlog.md` § M4 → `architecture.md` § 2–4 and § 10.
2. Continue with the first open step of the plan. One commit per backlog item after a green `make check`; the
   user allowed the agent to commit on `feat/m4-http-edge` without asking during M4.
3. **End of M4:** the docs review (`review.md`); then stop and tell the user: **no push and no PR** until the user
   has looked at the branch locally.

## Open questions (need a decision from the user)

- **Q10** — Review the M4 decisions taken while away, in `plans/M4-http-edge.md`: the schema changes D1–D9 and
  D11 (`tls`, `http.protocols`, `http.normalize_path`/`merge_slashes` on by default, the filters `client_ip`,
  `headers`, `cors`, `compression`, the `redirect` action, `php_workers.static`, the cluster types `static_files`
  and `http_proxy`). Recommendation: accept them as a whole with the PR.

## Resolved decisions

- 2026-10-25 — M3: the agent committed each item itself after a green `make check`; the user then left the rest of
  M3 to the agent ("finish it to the end"), the PR included. The choices made while the user was away (K-070,
  K-072, K-066, K-067 and the deviations in the plan's log) went in with the merge of PR #2.
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
- Docker image `golang:1.27` (pulled 2026-10-24) runs `make test-linux`; the `kest-test-php:8.3`, `8.4` and `8.5`
  images of `make test-matrix` are built from `php:<version>-cli` and `composer:2`, which BuildKit keeps in its
  cache rather than as tagged images
- Load testing: `test/bench/loadgen` (`make bench`), `ab` (ships with macOS); no `oha`/`wrk`. `make bench` needs
  RoadRunner's release binary (`RR=/path/to/rr`): none is installed; K-066 used 3.0.1 from the GitHub release.

## Session log (newest first, keep ~15)

- **2026-10-25** — M3 merged (PR #2, squash). M4 started on `feat/m4-http-edge` while the user was away: the plan
  (D1–D11), K-084 split into K-084/K-089/K-090; RoadRunner's `proxy_ip_parser` v6.0.0 (leftmost
  `X-Forwarded-For`, replaces the remote address) and the streaming protocol (pool v2.0.0-beta.1,
  `roadrunner/worker` 3.8.2) read in the sources.
- **2026-10-25** — M3, after the user went to bed and left the rest to the agent, PR included: K-070 body budget,
  K-072 `reload` (a baseline race found by `make test-matrix` on PHP 8.5, fixed), K-066 benchmark against
  RoadRunner 3.0.1 (K-069 dropped), K-067 operations guide; the docs review (19 findings, all fixed); PR #2.
- **2026-10-25** — M3 with the user: K-064 filters, K-075 `/config_dump`, K-065 server limits (a body read
  deadline from the route timeout: `net/http` cancels the request context on it, so `fail` took a slow client for
  a gone one), K-068 drain delay. Process: a `make check | grep` pipeline hid a lint failure once (a follow-up
  commit fixed it); commits now wait for `make check`'s own exit status (`rules.md` § 9).
- **2026-10-25** — M3 metrics and resets: ADR-0007 decided by experiment (`client_golang`, VictoriaMetrics,
  OpenTelemetry and a hand-written core compared; accepted with `expfmt` as a test-only oracle); the M3 plan
  approved; K-061 `internal/metrics` and instrumentation, K-062 `/metrics` and `/ready` per cluster, K-074
  `/clusters`, K-063 rolling reset with a surge (zero failed requests under load; a timestamp check broke under
  synctest's frozen clock, a generation counter replaced it).
- **2026-10-25** — M3 started on `feat/m3-operability`. The user's Symfony 8.1 app in `playground/` didn't start:
  `symfony/runtime` reads `APP_RUNTIME` before `.env`, so the app ran as a plain script and printed HTML to
  stdout, and kest's error showed 12 bytes of it. K-071: `worker.StdoutError` with the output and a `php_workers`
  hint. K-073: `test/docs` drift checks and the docs review (`review.md`, `docs-reviewer` agent), after this file
  still said "waiting for review" with M2 merged.
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

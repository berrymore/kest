# Status

_Last updated: 2026-10-26 (M6 unix socket listeners merged; no milestone scheduled: kest follows the user's app)_

## Current phase

**M6 (Unix socket listeners) is done and merged** (squashed into `main`): `address: unix:<path>` with `socket_mode`,
stale socket files handled, `client_ip` trusts `unix` peers (K-164). `plans/M6-unix-sockets.md` is the record,
including what was planned and shelved there. Before it, **M5 (Worker pools and routing to them)** made the pool the
core: lanes per route, overload control, a dynamic size, `sendfile`, richer route matches (`plans/M5-pools.md`).

**No milestone is scheduled after M6, and no release yet** (the user, 2026-10-26). The user builds an application on
kest and runs it for real; kest's work follows what that application needs. A release comes once kest has proved
itself there. The remaining milestones (RPC, dynamic configuration, WASM, gRPC) are candidates in `roadmap.md`.

**Roadmap reordered on 2026-10-26** (the user): M6 Unix socket listeners, M7 PHP → kest RPC, M8 Dynamic
configuration, M9 WASM sandbox, M10 gRPC. Prefork (K-154), socket relays (K-029) and WebSockets and SSE (K-163) went
to the icebox the same day, each after a look at what it would cost; their research stays (`research/prefork.md`,
`research/roadrunner.md` § Socket relays, `research/websockets.md`, ADR-0009 on hold). Milestone numbers in entries
before that date below follow the old order (M6 WASM, M7 Protocols, M8 Dynamic configuration).

## In progress

Nothing.

## Next up: how to continue in a fresh session

1. **Git first:** `git checkout main && git pull`; `git status` is clean, `git log --oneline -3` shows M6's squash
   commit on top. Work on kest from here starts on a branch named after the item (`feat/k<NNN>-<slug>`) or, for a
   batch, `feat/m<N>-<slug>` if the user makes it a milestone.
2. **Follow the user's application.** The user builds an application on kest in another repository. Expect requests
   from that use: a bug, a missing option, something RoadRunner does that kest doesn't. Put each in the backlog, agree
   the scope, then fix it. Candidates already known: K-153 (fewer hand-overs per job), K-150 (OPcache and JIT in the
   benchmark), the icebox (`backlog.md`), and the shelved features with their research (prefork, socket relays,
   WebSockets and SSE: ADR-0009 on hold).
3. **Q15** (below) is open: ask the user when it fits.

## Open questions (need a decision from the user)

- **Q15** — Cap kest's `GOMAXPROCS` by default? Measured (K-149, `docs/benchmarks.md` § kest's own CPU): on a host
  shared with the PHP workers, `GOMAXPROCS=2` to `4` cuts kest's CPU per request to a third or half and lets the
  pool serve 25–35 % more, since idle Go threads spin on cores PHP needs. Today it is advice in the operations guide
  (the Go runtime reads `GOMAXPROCS` from the environment). A default (say, `min(cores, 4)`, or a
  `server.max_procs`) would help most deployments but would starve a kest that does TLS and gzip for a busy site.
  The agent's suggestion: keep the advice, no default, until a deployment shows otherwise.

## Resolved decisions

- 2026-10-26 — No release milestone for now (the agent had suggested one: binaries, CI, framework guides): the user
  will build an application on kest and refine kest in real use; a release comes once that works. No milestone is
  scheduled after M6. The user had the docs review, `make test-matrix` and the PR run for M6.
- 2026-10-26 — WebSockets and SSE shelved (was Q16): the user liked ADR-0009's GRIP mechanism, then raised several
  instances (Nomad jobs) and blue-green deploys that close held connections, and decided to wait for a use case,
  since most PHP applications don't need SSE. The research and ADR-0009 (on hold) stay. M6 is the unix socket
  listeners alone (`plans/M6-unix-sockets.md`).
- 2026-10-26 — The M6 plan approved with D1's schema (`unix:` listener addresses, `socket_mode`,
  `client_ip`'s `unix`). The agent commits each item itself after a green `make check`, until the end of M6 (push and
  PR still on the user's OK).
- 2026-10-26 — Prefork and socket relays shelved (the user asked whether they were worth it): `relay: unix` is no
  faster than pipes; prefork saves a worker's boot (tens of milliseconds) and memory (unmeasured on real apps) at the
  cost of processes kest didn't spawn, a PHP package and fork safety that falls on the application. K-154 and K-029 to
  the icebox, the design sketch in `research/prefork.md` § Shelved. M6 is now WebSockets, SSE and unix sockets (K-164
  joins K-163); RPC M7, dynamic configuration M8, WASM M9, gRPC M10.
- 2026-10-26 — Roadmap reordered (the user): sockets and prefork (M6), WebSockets and SSE (M7) first, PHP → kest RPC
  a little later (M8, from the icebox), then dynamic configuration (M9), WASM (M10: "a fancy option, not a base
  feature for starting a PHP product") and gRPC last (M11). The user plans an RR/kest-first PHP framework in a
  separate repository, later; kest stays framework-agnostic. K-141 stays with WASM. (Renumbered the same day, see
  the entry above.)
- 2026-10-25 — The user checked `feat/m5-pools` locally ("all fine"), had it pushed, PR #5 opened, and merged
  (squash).
- 2026-10-25 — M5: the user lets the agent commit each item itself after a green `make check`, until the end of the
  milestone, then delegated all the remaining steps ("do all the steps"): the agent decides (D4's names and defaults
  included) and records the choices for the PR review. Push and PR only after the user's OK.

- 2026-10-25 — Q14: the user approved `plans/M5-pools.md` as a whole, with its schema changes (route `name`;
  `route.max_workers`, `priority`, `queue_timeout`; `php_workers.retry_after`; `pool.max_workers`,
  `pool.scale_down_after`; the `sendfile` filter; `match.regex`, `methods`, `headers`, `query`; a `route` label on the
  HTTP and pool metrics), and had it committed.

- 2026-10-25 — M5 scope (the user): the pool is the focus. K-142 weighted clusters dropped (one kest runs one release;
  A/B splits are sticky and PHP does them); K-145, K-146, K-147 to the icebox ("nice to have, the core first");
  K-141 to M6; the rest of M5 kept, plus K-151 (lanes, per-route worker limits) and K-152 (research: forking booted
  workers). M5 renamed "Worker pools and routing to them".

- 2026-10-25 — K-148: the user had it committed on its own branch, PR #4 opened and merged (squash) without
  stopping. K-150 (the benchmark with OPcache and JIT, was Q13): not now. msgpack: the user asked, the agent
  advised against it (`research/roadrunner.md`, the codec paragraph: `roadrunner/http` doesn't read it, goridge 4.6
  deprecates it, at most ~2 µs of the 4.5 µs `json_decode` takes per request); nothing planned.
- 2026-10-25 — The user merged PR #3 (M4). The schema changes D1–D8 and D12 (was Q10) went in with it as proposed.
  M6 WASM stays before M7 Protocols (was Q12).
- 2026-10-25 — Filters stay nested (listener → virtual host → route), rather than on listeners only with
  include/exclude paths in `static`: the router scopes every filter by host and path in one place, and a deny
  route can't be bypassed by a filter that keeps its own path list. Turning a listener filter off on a route stays
  K-141 (M5). The user then had the branch pushed and PR #3 opened.
- 2026-10-25 — ADR-0008 accepted: kest is an application server behind a proxy, not an edge proxy. M4 narrowed
  before its merge (`http_proxy` removed, static files as a filter on routes, filters on virtual hosts and routes);
  HTTP/2 and h2c are core, TLS and gzip stay as they are; dropped K-091, K-092, K-093, K-094, K-906. New
  milestones: M5 Routing, filters and pools; M7 Protocols (gRPC, WebSockets and SSE, unix sockets); WASM is M6,
  dynamic configuration M8. A FastCGI listener waits in the icebox as a bonus (K-911).
- 2026-10-25 — Q11 (add `static_files` to the glossary of `context.md`) is moot: the cluster type is gone. The
  glossary changed with ADR-0008.

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
- PHP 8.5.12 (brew, NTS) with `sockets`, `pcntl`, `posix`, `json`, `mbstring`, `openssl`, and `protobuf` 5.36.2
  (PECL, loaded by the first line of `/opt/homebrew/etc/php/8.5/php.ini`; added 2026-10-25); OPcache is loaded but
  `opcache.enable_cli` is off; Composer 2.10.3
- Also installed: Homebrew, Docker, git, gh (SSH protocol, logged in as `berrymore`)
- Docker image `golang:1.27` (pulled 2026-10-24) runs `make test-linux`; the `kest-test-php:8.3`, `8.4` and `8.5`
  images of `make test-matrix` are built from `php:<version>-cli` and `composer:2`, which BuildKit keeps in its
  cache rather than as tagged images
- Load testing: `test/bench/loadgen` (`make bench`), `ab` (ships with macOS); no `oha`/`wrk`. `make bench` needs
  RoadRunner's release binary (`RR=/path/to/rr`): none is installed; K-066 and K-148 downloaded 3.0.1 from the
  GitHub release (`gh release download v3.0.1 -R roadrunner-server/roadrunner`, sha256 checked) into a scratch
  directory. Without `ext-protobuf`: `PHPRC` pointing at a copy of `php.ini` without its first line.

## Session log (newest first, keep ~15)

- **2026-10-26** — Overview for the user; they plan a PHP framework of their own later, in another repository. The
  roadmap reordered on their call across roadmap, backlog, context, architecture, README and the ADR pointers. A
  plan for M6 "Unix sockets and prefork" drafted; RoadRunner's socket relays checked in source and by experiment
  (`research/roadrunner.md` § Socket relays: PHP connects lazily after the boot and retries for a millisecond, RR
  matches connections by pid and throws stdout away). Then the user questioned `relay: unix` and prefork: both
  shelved; M6 became WebSockets, SSE and unix sockets. K-164 unix socket listeners done; the WebSockets and SSE
  research (`research/websockets.md`) and ADR-0009 (SSE held through GRIP); the user then shelved WebSockets and
  SSE too (blue-green deploys and several instances need a broker and reconnect handling; no use case yet). M6 is the
  unix socket listeners alone, branch `feat/m6-unix-sockets`; docs review, `make test-matrix`, PR. No release and no
  milestone next: the user builds an application on kest and kest follows it.
- **2026-10-25** — M5 planned with the user (scope narrowed to the pool) and implemented on `feat/m5-pools` (the user
  delegated all steps, then left; they checked it locally, then PR #5, merged):
  K-910 route names; lanes in the pool (a dispatcher under one mutex, 3.1 → 1.8 µs per job) and on routes;
  `Retry-After`; overload control decided by experiment in virtual time and with PHP (`loadgen -rate`, new): without
  it overload collapses (7 of 40 requests/s served); dynamic pool size; `sendfile` (checked against Symfony's
  `BinaryFileResponse`); route matchers; K-095; K-149 (most of the server's CPU is Go's idle threads spinning:
  `GOMAXPROCS` 2–4 serves 25–35 % more on the laptop, Q15); K-152 research (forked workers: 1 ms and 4 MB against a
  35 ms boot and 27 MB); the docs review.
- **2026-10-25** — K-148: the user installed `ext-protobuf`; `make bench` re-run at M4 with and without it.
  With the extension `proto` is 1–4 % behind `json` (it was 30–35 %); PHP pays 14.4 µs per request for `json`,
  20.1 µs for `proto` with the extension, 135 µs without (`test/bench/codec.php`, new). RoadRunner is 8 % ahead on
  forms with 16 clients, confirmed by an interleaved re-run: kest's server takes more CPU per request (K-149).
  msgpack advised against; K-150 (OPcache and JIT) proposed, not now. PR #4, merged on the user's go-ahead (squash).
- **2026-10-25** — Kept filters nested after the user asked about listener-only filters with paths in `static`;
  PR #3 opened, then merged (squash) on the user's go-ahead, WASM kept before protocols. M5 not started, as asked.
- **2026-10-25** — The user looked at M4 and questioned the direction: kest was becoming an edge proxy. ADR-0008
  (accepted) positions it behind a proxy; `context.md`, roadmap and backlog follow (M5 routing, filters and pools;
  M7 protocols). M4 narrowed: K-085 `http_proxy` removed, K-096 filters on virtual hosts and routes (a filter's own
  answer counts without a cluster), K-082 redone as the `static` filter; operations guide: running behind nginx.

- **2026-10-25** — M3 merged (PR #2, squash). M4 done on `feat/m4-http-edge` while the user was away (no push, no
  PR): the plan (D1–D11, K-084 split into K-084/K-089/K-090), then K-080 TLS with certificate reload, K-081 HTTP/2,
  K-086 path normalization, K-087 `client_ip` (RoadRunner's `proxy_ip_parser` takes the spoofable leftmost
  `X-Forwarded-For`), K-084 `headers`, K-089 `cors`, K-090 `redirect`, K-083 `compression`, K-082 static files
  (`os.Root` dropped: it refuses Laravel's absolute `storage` link), K-088 streaming (checked against RR's pool
  v2 and `roadrunner/worker` 3.8.2; a dead worker's error wraps `io.EOF`, so a stream ends with `ok == false`),
  K-085 `http_proxy`; the docs review.
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

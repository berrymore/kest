# Backlog

States: `[ ]` todo · `[~]` in progress · `[x]` done · `[-]` dropped (say why).
IDs are never reused. Add new items at the end of the right section with the next free ID.
Milestone definitions: `roadmap.md`.

## M0 — Foundation

- [x] K-001 Repo skeleton: move `main.go` → `cmd/kest/main.go`, create `internal/` layout, Makefile
  (`build`, `test`, `test-integration`, `lint`, `fmt`), `.editorconfig`.
- [x] K-002 golangci-lint v2 config (`.golangci.yml`) + `make lint`; the Makefile checks that the
  installed linter version matches the pinned one (2.15.0) and prints install instructions if not.
- [x] K-004 CLI: `serve`, `validate [--dump]`, `version` (stdlib `flag` with subcommands; version info via `-ldflags`).
- [x] K-005 Choose a YAML library: `go.yaml.in/yaml/v4` rc.6 (ADR-0005).
- [x] K-006 Config loader: parse → env expansion in scalars → strict decode → defaults → validation with
  YAML paths; `config.Duration`, `config.ByteSize`; fuzz the env expansion.
- [x] K-007 Extension registry: generic categories (`http_filter`, `cluster`), duplicate detection, typed config decoding.
- [x] K-008 Server lifecycle: build graph, ordered start/stop, SIGINT/SIGTERM, drain timeout, forced exit on second signal.
- [x] K-009 Logging: slog setup from config (level, format), component-scoped loggers.
- [x] K-010 Admin server: `/ready`, `/server_info`; binds to 127.0.0.1 by default.
- [x] K-011 HTTP listener + connection manager + router (virtual hosts by `domains` with wildcards;
  `prefix`/`path` match) + `direct_response` action.
- [x] K-012 `examples/hello/kest.yaml` + e2e test (build the binary, start it, curl it, SIGTERM it).
- [x] K-013 `docs/config.md` skeleton: config reference for everything that exists after M0.
- [x] K-014 License file: MIT (2026-10-24).
- [x] K-015 `make check`: build + vet + lint + `test -race` + e2e in one target; the local stand-in for CI.
- [ ] K-016 Upgrade `go.yaml.in/yaml/v4` from rc.6 to v4.0.0 GA once released; rerun config tests.
- [x] K-017 Goroutine leak checks in tests: `internal/leaktest` on top of `go.uber.org/goleak`, used by
  `listener` and `server` tests.

## M1 — Worker pool and relay

- [x] K-020 Research: goridge frame layout, flags, CRC, options; RR worker handshake (pid), stop and
  error signaling; pool and supervisor behavior. Verified against RR v3.0.1 (goridge v4, pool v2) and
  `roadrunner/worker` 3.8.2, plus an experiment → `research/roadrunner.md` (2026-10-24).
- [x] K-021 Frame codec (encode/decode) + fuzz tests + benchmarks.
- [x] K-022 Pipes relay (child stdin/stdout); worker stderr → logs.
- [x] K-023 Worker process: spawn with explicit env, handshake with timeout, exec with deadline, graceful stop, kill.
- [x] K-024 Fake worker in Go (`internal/worker/workertest`): echo, slow, crash, leak-memory, bad-frame modes.
- [x] K-025 Pool: fixed size, bounded wait queue, `allocate_timeout`, `max_jobs`, `destroy_timeout`.
- [x] K-026 Supervisor: `ttl`, `idle_ttl`, `exec_ttl`, `max_worker_memory` (RSS sampling on linux/macOS).
- [x] K-027 Respawn with exponential backoff; pool health state.
- [x] K-028 Integration test: PHP echo worker on `roadrunner/worker` (formerly `spiral/roadrunner-worker`;
  `testdata/php/` + composer; pin the package versions in `composer.lock`).

## M2 — PHP HTTP (MVP)

- [x] K-040 Research: RR HTTP payload (JSON vs protobuf request context, uploads, cookies, attributes,
  response format, streaming/chunked responses), RR `rpc` plugin usage by PHP libraries (Q-G). Verified against
  RR http plugin v6.0.0 and `roadrunner/http` 4.2.1, plus an experiment → `research/roadrunner.md` (2026-10-25).
- [x] K-041 `php_workers` cluster type: config, defaults, validation, wiring to pool. Decide the worker
  environment policy (inherit kest's environment like RR, or explicit only) and `RR_MODE`/`RR_VERSION`.
  Inherit by default (`inherit_env`), `RR_MODE=http`, no `RR_VERSION`; paths relative to the config file
  (plan D2–D4).
- [x] K-042 Request encoding: method, URI, headers, cookies, query, body (bounded), remote address, TLS info.
  `internal/rrhttp`: JSON and protobuf contexts, PHP form trees (checked against RR's code by a differential fuzz).
- [x] K-043 Multipart uploads: temp files, size limits, cleanup. `uploads: {dir, forbid, allow}`; files stream to
  disk (one copy), PHP's `UPLOAD_ERR_*` codes as in RR; temp files removed after the response and on every error.
- [x] K-044 Response decoding: status, multi-value headers, body; error mapping (worker error → 5xx).
  `rrhttp.DecodeResponse` (JSON and protobuf), `WriteResponse` (framing, header hygiene, trailers), `ErrorStatus`.
- [x] K-045 `examples/php-hello`: composer.json + PSR-7 worker + kest.yaml. E2e test with the real binary
  (`-tags e2e,integration`, run by `make test-integration`).
- [x] K-046 Framework smoke test (Symfony RoadRunner runtime or Laravel Octane RR worker). Symfony 7.4 on
  `runtime/roadrunner-symfony-nyholm` 1.0.0 (`spiral/roadrunner-http` 3.6), both codecs: `test/e2e/symfony_test.go`.
- [x] K-047 Docker e2e environment with a PHP version matrix (8.3 / 8.4 / 8.5), runnable locally;
  reused by CI later. `make test-matrix`: `test/docker` image (php:<v>-cli + sockets, Go,
  Composer) runs `make test-integration` on a copy of the tree; green on 8.3.36, 8.4.27, 8.5.12 (linux/arm64).
- [x] K-048 Getting-started guide in `docs/`. `docs/getting-started.md` (checked by running it verbatim),
  README, AGENTS.md.

## M3 — Operability

- [x] K-060 Choose the metrics approach (Q-E) → ADR. ADR-0007, accepted 2026-10-25: a hand-written core in
  `internal/metrics` with the Prometheus text format, `expfmt` as a test-only oracle; measured against
  `client_golang`, VictoriaMetrics/metrics and OpenTelemetry.
- [x] K-061 Metrics: requests, latency, status per listener/route/cluster; pool queue; worker states; restarts; memory.
  `internal/metrics` (fuzzed against `expfmt`, 7 ns per counter + histogram update), `kest_http_*` per listener,
  virtual host, cluster and code, `kest_pool_*` through `worker.Observer` and scrape-time collectors, a subset of
  `go_*`/`process_*` (RSS on Linux only).
- [x] K-062 Admin: `/metrics` (split on 2026-10-25: `/clusters` is K-074, `/config_dump` K-075,
  `POST /clusters/{name}/reset` belongs to K-063). `/ready` must use `worker.Health() == Unhealthy` only: `Degraded` is normal for a moment whenever a worker is
  recycled (max_jobs, ttl).
  Done: `GET /metrics`; `/ready` answers `503 not serving: <clusters>` through the optional `cluster.Prober`;
  `docs/metrics.md`.
- [x] K-063 Rolling reset without failed requests (admin and SIGHUP). `Pool.Reset` with a surge (D4); the e2e
  test serves ~13 000 requests from 16 clients during an admin reset and a SIGHUP reset of `php-hello` with none
  failed; a reset to code that doesn't boot stops and leaves the old workers serving.
- [x] K-064 Filters: `access_log`, `request_id`, `recovery`. In `internal/filters/`, on `httpx.RequestInfo` and
  `httpx.ResponseWriter`; `examples/php-hello` turns them on, and its e2e test sees the ID in PHP, in the response
  and in the access log.
- [x] K-065 Per-route timeouts; max header and body sizes; slow-client protection (server timeouts).
  `http.read_header_timeout`, `read_timeout`, `write_timeout`, `idle_timeout`, `max_header_bytes` per listener
  (defaults: the old fixed values); `php_workers` stops reading a body at the route timeout (`408`). Route
  timeouts and `max_request_body` existed already.
- [x] K-066 Benchmarks against RoadRunner; report in `docs/benchmarks.md`. `make bench` (`test/bench`: configs,
  `loadgen`, `run.sh`) against RoadRunner 3.0.1: even with the same codec, 42–54 % ahead on small requests with
  kest's default `json`, half the memory with 1 MB bodies.
- [x] K-067 Operations guide. `docs/operations.md`: running under systemd, signals, probes, deploys with rolling
  resets, sizing, monitoring, logs, troubleshooting.
- [-] K-069 Request frames copy context + body into one payload (one copy of the body per job). If K-066
  shows it matters, write the frame in segments instead (`relay.Writer` with several payload parts). Dropped on
  2026-10-25: with 1 MB bodies K-066 found kest 3–11 % ahead of RoadRunner, at half its memory; reopen if a
  profile shows the copy.
- [x] K-068 Drain delay: after SIGTERM, keep serving with `/ready` = 503 for a configurable delay so load
  balancers deregister the instance before listeners close. `server.drain_delay` (default `0s`); not on a
  fatal error.
- [x] K-070 Bound the memory held by buffered request bodies: `php_workers` reads a body (up to
  `max_request_body`) before it queues for a worker, so up to `(workers + max_queue_size) × max_request_body`
  can be in memory (16 GB with the defaults and 1000 waiting uploads). Admit before reading the body, or cap
  the buffered bytes per cluster. (Found in M2; RR reads up to 1000 MB per request the same way.)
  Done: `max_buffered_bodies` (default 256MB), reserved before the body is read; `503` and
  `kest_pool_rejected_total{reason="body_budget"}` when full.
- [x] K-071 Clear error when a worker prints to stdout outside the protocol: show what it printed (the rejected
  header plus what is left in the pipe, up to 256 bytes; also when the pipe broke because a plain script printed
  and exited) and, from `php_workers`, a hint: no RoadRunner loop, `display_errors=stderr`, Symfony's
  `APP_RUNTIME` in `env` or `extra.runtime.class` rather than `.env` (`research/roadrunner.md`, Framework
  runtimes). Found on 2026-10-25 with a Symfony app in `playground/`.
- [x] K-072 Development mode: replace workers when PHP files change (RoadRunner's `reload` plugin): watched
  directories and patterns, debounce, a rolling replacement on top of K-063. Until then code changes need a
  restart of kest. Added to M3 with the user's OK on 2026-10-25. Done: `php_workers.reload` (`dirs`, `patterns`,
  `interval`, `max_files`), a scan by name, size and mtime, one reset once a change holds still.
- [x] K-073 Docs drift checks and the milestone docs review: `test/docs` (`make docs-check`, part of
  `make test`) checks links, make targets, IDs, the ADR index, the layout, the config reference, tool versions,
  status dates; `.ai/review.md` and the `docs-reviewer` agent run before every milestone PR and merge. Asked for
  on 2026-10-25 after `status.md` still said "waiting for review" with M2 merged.
- [x] K-074 Admin `/clusters`: each cluster's type and health, and its workers (slot, state, pid, jobs, age,
  memory), from an optional interface that clusters implement. `cluster.Reporter`; the php-hello e2e test checks
  the pids against the workers that served requests.
- [x] K-075 Admin `/config_dump`: the effective config as YAML, with the `env` values of clusters redacted.
  `config.Redactor` and `Config.DumpRedacted`; an undecoded block is hidden whole.

## M4 — HTTP edge features

- [x] K-080 TLS listeners from cert files. `listeners[].tls` (`certificates` picked by SNI, `min_version`), loaded
  when the graph is built (located errors in `kest validate`), checked every 10s and reloaded once a change holds
  still; `kest_tls_certificate_expiry_timestamp_seconds`, `kest_tls_handshake_errors_total`.
- [x] K-081 HTTP/2 (h2 over TLS, h2c). `listeners[].http.protocols` (`http1`, `http2`; both with `tls`, `http1`
  without); h2c is prior knowledge through `http.Protocols`; the PHP e2e test sees protocol `2` both ways.
- [x] K-082 Static files route action / filter. Done as the `static` filter (`dir`, `forbid: [.php]`, `headers`;
  the file if there is one, else the rest of the chain), set on the route to PHP (K-096): hidden paths refused
  except `/.well-known/`, symlinks followed, `http.ServeContent` with a weak ETag, `dir` checked when the graph is
  built; `examples/php-hello` serves `public/`. First built as `php_workers.static` and a `static_files` cluster,
  both removed before the merge (ADR-0008).
- [x] K-083 Compression filter. `compression`: gzip with pooled writers, `level`, `min_length`, `content_types`;
  the decision waits for the first write (sniffed Content-Type), flushes pass through, `HEAD`/`204`/`206`/`304`/
  `no-transform`/encoded responses are left alone.
- [x] K-084 Header manipulation filter (`headers`). Split on 2026-10-25: CORS is K-089, redirects K-090.
  `request`/`response` rules `remove`, `set`, `add`, `set_if_absent`; protected framing headers rejected;
  `httpx.BeforeHeader` edits a response header just before it goes out (shared with CORS and compression).
- [-] K-085 `http_proxy` cluster: endpoints, load balancing, health checks, retries. Built on 2026-10-25 (round
  robin / least request / random, active health checks, retries, upstream TLS, `kest_upstream_*` metrics), then
  removed before the merge: proxying to other services is the job of the proxy in front of kest (ADR-0008).
- [x] K-086 Path normalization before routing (dot segments, duplicate slashes; Envoy `normalize_path` /
  `merge_slashes`), so prefix routes can't be bypassed with `/a/../b`. `listeners[].http.normalize_path` and
  `merge_slashes`, both on by default, on the decoded path before the filters; a changed path is rewritten for
  the cluster too (fuzzed: idempotent, no dot segments or `//` left).
- [x] K-087 Trusted proxies: client IP from `X-Forwarded-For` / `Forwarded` for trusted peers, passed to PHP as
  the `ipAddress` attribute that `roadrunner/http` reads (RR's `proxy_ip_parser` middleware). Done as the
  `client_ip` filter: right-to-left walk over trusted hops, `X-Forwarded-Proto`; PHP gets the IP in `remoteAddr`
  (what RR's `proxy_ip_parser` v6 does, not an attribute) and the scheme in the URI; `httpx.ClientIP`/`Scheme`.
- [x] K-088 Streaming responses from PHP workers (`STREAM` frames, `PSR7Worker::$chunkSize`, generator bodies)
  and 1xx informational responses (103 Early Hints); stop a stream when the client goes away (Q-C).
  `worker.Pool.ExecStream`/`Stream` (PING→PONG, STOP and drain, `exec_ttl` per frame), the fake worker streams like
  `roadrunner/worker`; `php_workers` flushes each chunk, aborts the connection on a failure after the headers; PHP
  integration test with a generator, early hints, `$chunkSize` and a client that disconnects, on both codecs.
- [x] K-089 CORS filter (`cors`): preflight answered by kest, allowed origins with wildcards.
- [x] K-090 Redirect route action (`redirect`): scheme, host, port, path, prefix rewrite, status.
- [-] K-091 Client certificates on TLS listeners (mTLS): a CA file, required or optional, the verified subject
  passed to PHP. Dropped: the proxy in front or a mesh sidecar checks client certificates (ADR-0008).
- [-] K-092 Brotli and zstd in `compression` (each needs a dependency: no encoder in the standard library).
  Dropped: an edge feature; `compression` stays gzip only (ADR-0008).
- [-] K-093 Precompressed static files: serve `file.gz` (and `.br`) next to `file` when the client accepts it.
  Dropped: static files stay basic (ADR-0008).
- [-] K-094 Passive health checks for `http_proxy` (outlier detection). Dropped with K-085 (ADR-0008).
- [x] K-096 Filters on virtual hosts and routes (`virtual_hosts[].filters`, `routes[].filters`), run after the
  router has chosen them; a response one of them makes counts with an empty `cluster` (ADR-0008). The router
  instruments each virtual host and a route's cluster marks the request (`RequestInfo`), so the metrics need no
  lookup; `/config_dump` redacts their configs too.

## M5 — Worker pools and routing to them

IDs start at K-140: the milestones were renumbered on 2026-10-25 (ADR-0008), and WASM keeps its K-100 range. Plan:
`plans/M5-pools.md`. Narrowed with the user on 2026-10-25: the pool is the focus.

- [x] K-151 Lanes: a route's own queue in its cluster's pool, with `max_workers` (at most that many of the pool's
  workers serve the route at once) and a `queue_timeout` (plan D2, D3; 2026-10-25).
- [x] K-905 Per-route priority within one pool (Q-F): a free worker takes the oldest request of the highest-priority
  lane (plan D2; 2026-10-25).
- [x] K-144 Fast rejection under overload: `Retry-After` on every `503` of the pool, per-route `queue_timeout`, and
  overload control (CoDel, `pool.overload`, on by default at 500ms / 5s after a measurement; plan D3, D4;
  2026-10-25).
- [x] K-908 Dynamic pool size: extra workers start as soon as requests wait, not after `allocate_timeout` as in
  RoadRunner's `pool.dynamic_allocator`, and stop when idle (`pool.max_workers`, `scale_down_after`; plan D5;
  2026-10-25).
- [x] K-143 `sendfile` filter: PHP answers `X-Sendfile`, kest sends the file from configured directories, with
  ranges and conditional requests (plan D6; RoadRunner's `send` middleware ✅ in `research/roadrunner.md`;
  2026-10-25).
- [x] K-140 Route matches on method, headers and query parameters, and regular-expression paths (RE2) (plan D7;
  2026-10-25).
- [x] K-910 Optional route `name`, unique in the config, and a `route` label on the HTTP metrics and in
  `access_log` (plan D1; 2026-10-25).
- [x] K-095 Count requests that listener filters answer themselves (CORS preflights) in the HTTP metrics, with an
  empty virtual host, route and cluster. Moved from M4 on 2026-10-25 (plan D8; done 2026-10-25).
- [x] K-152 Research: start workers by forking a booted PHP process (2026-10-25, `research/prefork.md`): on the
  Symfony smoke app a fork is ready in about 1 ms against a 35–43 ms boot, and 9 forked processes take 36 MB PSS
  against about 248 MB. Worth an opt-in mode (K-154).
- [x] K-148 Benchmark the codecs with `ext-protobuf` (2026-10-25, before M5 started): `docs/benchmarks.md` re-run
  at M4 with and without the extension; `test/bench/codec.php` measures what each codec costs PHP. With the
  extension `json` stays 1–4 % ahead of `proto`, and kest and RoadRunner are even except on forms (K-149).
- [x] K-149 Trim kest's own CPU per request (2026-10-25: profiled; buffered relay reads; `GOMAXPROCS` advice, see
  `docs/benchmarks.md` § kest's own CPU; the rest is K-153). Was: K-148 measured the server at 77–86 µs per small request against
  RoadRunner's 72 µs, and 95–105 µs per small form against 79 µs; RoadRunner then serves about 8 % more forms with
  16 clients (at M3, without the extension, the two were even on forms: 28 526 against 28 543 requests/s with
  `proto`). Profile the form path (`rrhttp` form tree, `encoding/json`) and what M4 added per request (path
  normalization, the filter chain on routes) before changing anything.
- [ ] K-153 Fewer goroutine hand-overs per request: today a job goes to the slot's goroutine, which talks to the
  worker, and the result comes back, two wake-ups that RoadRunner's handler (which talks to the worker itself)
  doesn't pay; the scheduler is 45 % of the server's CPU (K-149). Option: the caller borrows the slot's process for
  the exchange and does the slot's bookkeeping after it. Changes the pool's ownership model: an ADR first, and
  `BenchmarkPoolExec` plus the server CPU per request (`docs/benchmarks.md`) before and after.
- [ ] K-150 Benchmark with OPcache and JIT on the CLI (`opcache.enable_cli=1`, `opcache.jit=tracing`): the
  benchmark runs PHP without them, and the PSR-7 code `roadrunner/http` runs per request may gain more from JIT
  than from any codec. Proposed on 2026-10-25; the user: not now.

- [-] K-142 Weighted clusters. Dropped on 2026-10-25: one kest runs one release of one application, so a split
  between releases belongs to the proxy or the orchestrator, and an A/B split needs a sticky assignment that PHP
  does itself (ADR-0008).

## M6 — Unix socket listeners

Reordered with the user on 2026-10-26 (`roadmap.md`): prefork and socket relays, then WebSockets and SSE, were planned
here and went to the icebox the same day. Plan: `plans/M6-unix-sockets.md`.

- [x] K-163 Research and ADR: WebSockets and SSE held by kest while PHP handles the events (2026-10-26:
  `research/websockets.md`; ADR-0009 proposes SSE streams held through a GRIP subset). The feature itself was then
  shelved: K-165.
- [x] K-164 Unix socket listeners (`address: unix:/run/kest.sock`, `socket_mode`), for a proxy on the same host;
  `client_ip` trusts `unix` peers (plan D1; 2026-10-26). The admin server stays on TCP: nothing asked for a socket.

## M7 — PHP → kest RPC

- [ ] K-900 PHP → kest RPC channel (RoadRunner `rpc` plugin equivalent). From the icebox on 2026-10-26.

## M8 — Dynamic configuration

- [ ] K-120 Design ADR: reload model, graph diffing, listener handover.
- [ ] K-121 Reload on SIGHUP / file change with diff-based apply.

## M9 — WASM sandbox

- [ ] K-100 Research and ADR: guest ABI (Proxy-Wasm, http-wasm, WASI stdin/stdout, custom).
- [ ] K-101 wazero runtime, compilation cache, instance pool, memory and time limits.
- [ ] K-102 `wasm` cluster type.
- [ ] K-103 `wasm` HTTP filter.
- [ ] K-104 Example modules (Go/TinyGo, Rust) + benchmarks.
- [ ] K-141 Per-route filter settings: turn a listener filter off, or change its config, on a virtual host or a
  route (Envoy's `typed_per_filter_config`). Moved from M5 on 2026-10-25: the `wasm` filter extends the filter
  model here.

## M10 — gRPC

- [ ] K-160 Research and ADR: gRPC in kest. RoadRunner's `grpc` plugin ❓ (what PHP gets, which call types),
  the codec to PHP workers, gRPC statuses for kest's own errors (`RESOURCE_EXHAUSTED`, `DEADLINE_EXCEEDED`),
  `grpc-timeout` as the request's deadline.
- [ ] K-161 gRPC unary calls to `php_workers` on the HTTP/2 listener, routed like any request.
- [ ] K-162 gRPC server streaming over streamed worker responses (K-088).

## Deferred

- [ ] K-003 CI (GitHub Actions): build, vet, lint, `test -race` on linux and macOS; PHP integration matrix.
  Deferred on 2026-10-24: single developer, local workflow. Revisit before the first release or when a
  second contributor joins.

## Ideas / icebox

- [ ] K-901 Queues/jobs plugin.
- [-] K-902 gRPC listener with PHP workers. Superseded by M10 (K-160…K-162).
- [ ] K-903 systemd notify and socket activation.
- [ ] K-904 Envoy-style hot restart (FD passing to the new process).
- [-] K-906 ACME (automatic TLS certificates). Dropped: the proxy in front gets the certificates (ADR-0008).
- [ ] K-907 Run workers as another user/group (RR `server.user` / `server.group`).
- [ ] K-909 One-off command before workers start (RR `server.on_init`).
- [ ] K-911 FastCGI listener: nginx's `fastcgi_pass` to kest in place of PHP-FPM, without touching the nginx
  config (`net/http/fcgi`; RoadRunner's `http.fcgi` ❓). A bonus, not a priority (2026-10-25).
- [ ] K-145 Authentication filters: JWT verification (keys from a file or a JWKS URL) and API keys; the verified
  claims reach PHP as headers. Moved from M5 on 2026-10-25: nice to have, the pool comes first.
- [ ] K-146 Local rate limiting filter: token buckets keyed by client IP, a header or the route. Moved from M5 on
  2026-10-25.
- [ ] K-147 External authorization through a cluster: a subrequest to a small PHP pool decides, and the headers it
  returns go to the main request (Envoy's `ext_authz`). Moved from M5 on 2026-10-25.
- [ ] K-029 Unix socket relays between kest and its workers (`RR_RELAY=unix://…`). Deferred in M1; scheduled for M6
  and shelved again on 2026-10-26: no faster than pipes, and only prefork needed them. RoadRunner's are described in
  `research/roadrunner.md` § Socket relays.
- [ ] K-154 Prefork mode (opt-in): a booted PHP "template" forks the workers of a pool on kest's request; each child
  connects over a unix socket of its own (K-029). From K-152, 2026-10-25. Shelved on 2026-10-26 with the user: the
  gain is memory density (unmeasured on real applications) and a worker's boot, the cost is processes kest didn't
  spawn, a PHP package, and fork safety that falls on the application. Revisit when a deployment runs short of memory,
  or when a framework designed to be fork-safe can make it safe by default. Design sketch in `research/prefork.md`.
- [ ] K-165 WebSockets and SSE held by kest while PHP handles the events: the feature K-163 researched. ADR-0009
  (proposed, on hold) has the design, plus what a production version needs from the start: a broker for several
  instances and blue-green deploys, and reconnects as a normal case (plan `M6-unix-sockets.md` D2). Shelved on
  2026-10-26 with the user: most PHP applications don't need it; revisit with a use case.

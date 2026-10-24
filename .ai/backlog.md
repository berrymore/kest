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
- [ ] K-029 Unix socket / TCP relays (only if needed before M3).

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
- [x] K-047 Docker e2e environment with a PHP version matrix (8.3 / 8.4 / 8.5), runnable locally
  (`make e2e-matrix`); reused by CI later. `make test-matrix`: `test/docker` image (php:<v>-cli + sockets, Go,
  Composer) runs `make test-integration` on a copy of the tree; green on 8.3.36, 8.4.27, 8.5.12 (linux/arm64).
- [x] K-048 Getting-started guide in `docs/`. `docs/getting-started.md` (checked by running it verbatim),
  README, AGENTS.md.

## M3 — Operability

- [ ] K-060 Choose the metrics approach (Q-E) → ADR.
- [ ] K-061 Metrics: requests, latency, status per listener/route/cluster; pool queue; worker states; restarts; memory.
- [ ] K-062 Admin: `/config_dump` (with redaction), `/clusters`, `POST /clusters/{name}/reset`, `/metrics`.
  `/ready` must use `worker.Health() == Unhealthy` only: `Degraded` is normal for a moment whenever a worker is
  recycled (max_jobs, ttl).
- [ ] K-063 Rolling reset without failed requests (admin and SIGHUP).
- [ ] K-064 Filters: `access_log`, `request_id`, `recovery`.
- [ ] K-065 Per-route timeouts; max header and body sizes; slow-client protection (server timeouts).
- [ ] K-066 Benchmarks against RoadRunner; report in `docs/benchmarks.md`.
- [ ] K-067 Operations guide.
- [ ] K-069 Request frames copy context + body into one payload (one copy of the body per job). If K-066
  shows it matters, write the frame in segments instead (`relay.Writer` with several payload parts).
- [ ] K-068 Drain delay: after SIGTERM, keep serving with `/ready` = 503 for a configurable delay so load
  balancers deregister the instance before listeners close.
- [ ] K-070 Bound the memory held by buffered request bodies: `php_workers` reads a body (up to
  `max_request_body`) before it queues for a worker, so up to `(workers + max_queue_size) × max_request_body`
  can be in memory (16 GB with the defaults and 1000 waiting uploads). Admit before reading the body, or cap
  the buffered bytes per cluster. (Found in M2; RR reads up to 1000 MB per request the same way.)

## M4 — HTTP edge features

- [ ] K-080 TLS listeners from cert files.
- [ ] K-081 HTTP/2 (h2 over TLS, h2c).
- [ ] K-082 Static files route action / filter.
- [ ] K-083 Compression filter.
- [ ] K-084 CORS and header-manipulation filters; redirect action.
- [ ] K-085 `http_proxy` cluster: endpoints, load balancing, health checks, retries.
- [ ] K-086 Path normalization before routing (dot segments, duplicate slashes; Envoy `normalize_path` /
  `merge_slashes`), so prefix routes can't be bypassed with `/a/../b`.
- [ ] K-087 Trusted proxies: client IP from `X-Forwarded-For` / `Forwarded` for trusted peers, passed to PHP as
  the `ipAddress` attribute that `roadrunner/http` reads (RR's `proxy_ip_parser` middleware).
- [ ] K-088 Streaming responses from PHP workers (`STREAM` frames, `PSR7Worker::$chunkSize`, generator bodies)
  and 1xx informational responses (103 Early Hints); stop a stream when the client goes away (Q-C).

## M5 — WASM sandbox

- [ ] K-100 Research and ADR: guest ABI (Proxy-Wasm, http-wasm, WASI stdin/stdout, custom).
- [ ] K-101 wazero runtime, compilation cache, instance pool, memory and time limits.
- [ ] K-102 `wasm` cluster type.
- [ ] K-103 `wasm` HTTP filter.
- [ ] K-104 Example modules (Go/TinyGo, Rust) + benchmarks.

## M6 — Dynamic configuration

- [ ] K-120 Design ADR: reload model, graph diffing, listener handover.
- [ ] K-121 Reload on SIGHUP / file change with diff-based apply.

## Deferred

- [ ] K-003 CI (GitHub Actions): build, vet, lint, `test -race` on linux and macOS; PHP integration matrix.
  Deferred on 2026-10-24: single developer, local workflow. Revisit before the first release or when a
  second contributor joins.

## Ideas / icebox

- [ ] K-900 PHP → kest RPC channel (RoadRunner `rpc` plugin equivalent).
- [ ] K-901 Queues/jobs plugin.
- [ ] K-902 gRPC listener with PHP workers.
- [ ] K-903 systemd notify and socket activation.
- [ ] K-904 Envoy-style hot restart (FD passing to the new process).
- [ ] K-905 Per-route priority queues within one pool (Q-F).
- [ ] K-906 ACME (automatic TLS certificates).
- [ ] K-907 Run workers as another user/group (RR `server.user` / `server.group`).
- [ ] K-908 Dynamic pool size (RR `pool.dynamic_allocator`).
- [ ] K-909 One-off command before workers start (RR `server.on_init`).

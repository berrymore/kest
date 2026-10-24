# Backlog

States: `[ ]` todo · `[~]` in progress · `[x]` done · `[-]` dropped (say why).
IDs are never reused. Add new items at the end of the right section with the next free ID.
Milestone definitions: `roadmap.md`.

## M0 — Foundation

- [ ] K-001 Repo skeleton: move `main.go` → `cmd/kest/main.go`, create `internal/` layout, Makefile
  (`build`, `test`, `test-integration`, `lint`, `fmt`), `.editorconfig`.
- [ ] K-002 golangci-lint v2 config (`.golangci.yml`) + `make lint`.
- [ ] K-004 CLI: `serve`, `validate [--dump]`, `version` (stdlib `flag` with subcommands; version info via `-ldflags`).
- [ ] K-005 Choose a YAML library (`go.yaml.in/yaml/v3`/`v4` vs `goccy/go-yaml`): strict mode, node
  line/column for errors, maintenance status. Record the choice in ADR-0005.
- [ ] K-006 Config loader: parse → env expansion in scalars → strict decode → defaults → validation with
  YAML paths; `config.Duration`, `config.ByteSize`; fuzz the env expansion.
- [ ] K-007 Extension registry: generic categories (`http_filter`, `cluster`), duplicate detection, typed config decoding.
- [ ] K-008 Server lifecycle: build graph, ordered start/stop, SIGINT/SIGTERM, drain timeout, forced exit on second signal.
- [ ] K-009 Logging: slog setup from config (level, format), component-scoped loggers.
- [ ] K-010 Admin server: `/ready`, `/server_info`; binds to 127.0.0.1 by default.
- [ ] K-011 HTTP listener + connection manager + router (virtual hosts by `domains` with wildcards;
  `prefix`/`path` match) + `direct_response` action.
- [ ] K-012 `examples/hello/kest.yaml` + e2e test (build the binary, start it, curl it, SIGTERM it).
- [ ] K-013 `docs/config.md` skeleton: config reference for everything that exists after M0.
- [x] K-014 License file: MIT (2026-10-24).
- [ ] K-015 `make check`: build + vet + lint + `test -race` in one target; the local stand-in for CI.

## M1 — Worker pool and relay

- [ ] K-020 Research: goridge v3 frame layout, flags, CRC, options; RR worker handshake (pid), stop and
  error signaling. Verify against `spiral/goridge` (Go and PHP) and `roadrunner-server/pool` sources →
  `research/roadrunner.md` (✅ with links).
- [ ] K-021 Frame codec (encode/decode) + fuzz tests + benchmarks.
- [ ] K-022 Pipes relay (child stdin/stdout); worker stderr → logs.
- [ ] K-023 Worker process: spawn with explicit env, handshake with timeout, exec with deadline, graceful stop, kill.
- [ ] K-024 Fake worker in Go (`internal/worker/workertest`): echo, slow, crash, leak-memory, bad-frame modes.
- [ ] K-025 Pool: fixed size, bounded wait queue, `allocate_timeout`, `max_jobs`, `destroy_timeout`.
- [ ] K-026 Supervisor: `ttl`, `idle_ttl`, `exec_ttl`, `max_worker_memory` (RSS sampling on linux/macOS).
- [ ] K-027 Respawn with exponential backoff; pool health state.
- [ ] K-028 Integration test: PHP echo worker on `spiral/roadrunner-worker` (`testdata/php/` + composer;
  pin the package versions in `composer.lock`).
- [ ] K-029 Unix socket / TCP relays (only if needed before M3).

## M2 — PHP HTTP (MVP)

- [ ] K-040 Research: RR HTTP payload (JSON vs protobuf request context, uploads, cookies, attributes,
  response format, streaming/chunked responses), RR `rpc` plugin usage by PHP libraries (Q-G).
- [ ] K-041 `php_workers` cluster type: config, defaults, validation, wiring to pool.
- [ ] K-042 Request encoding: method, URI, headers, cookies, query, body (bounded), remote address, TLS info.
- [ ] K-043 Multipart uploads: temp files, size limits, cleanup.
- [ ] K-044 Response decoding: status, multi-value headers, body; error mapping (worker error → 5xx).
- [ ] K-045 `examples/php-hello`: composer.json + PSR-7 worker + kest.yaml.
- [ ] K-046 Framework smoke test (Symfony RoadRunner runtime or Laravel Octane RR worker).
- [ ] K-047 Docker e2e environment with a PHP version matrix (8.3 / 8.4 / 8.5), runnable locally
  (`make e2e-matrix`); reused by CI later.
- [ ] K-048 Getting-started guide in `docs/`.

## M3 — Operability

- [ ] K-060 Choose the metrics approach (Q-E) → ADR.
- [ ] K-061 Metrics: requests, latency, status per listener/route/cluster; pool queue; worker states; restarts; memory.
- [ ] K-062 Admin: `/config_dump` (with redaction), `/clusters`, `POST /clusters/{name}/reset`, `/metrics`.
- [ ] K-063 Rolling reset without failed requests (admin and SIGHUP).
- [ ] K-064 Filters: `access_log`, `request_id`, `recovery`.
- [ ] K-065 Per-route timeouts; max header and body sizes; slow-client protection (server timeouts).
- [ ] K-066 Benchmarks against RoadRunner; report in `docs/benchmarks.md`.
- [ ] K-067 Operations guide.

## M4 — HTTP edge features

- [ ] K-080 TLS listeners from cert files.
- [ ] K-081 HTTP/2 (h2 over TLS, h2c).
- [ ] K-082 Static files route action / filter.
- [ ] K-083 Compression filter.
- [ ] K-084 CORS and header-manipulation filters; redirect action.
- [ ] K-085 `http_proxy` cluster: endpoints, load balancing, health checks, retries.

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

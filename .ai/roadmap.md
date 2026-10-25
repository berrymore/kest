# Roadmap

Milestones are ordered; each has scope, exit criteria and explicit out-of-scope items.
Work items live in `backlog.md` (IDs in brackets). Reprioritization is the user's call.

| Milestone | Theme | Status |
|---|---|---|
| M0 | Foundation | ✅ done 2026-10-24 |
| M1 | Worker pool and relay | ✅ done 2026-10-25 |
| M2 | PHP HTTP — **MVP** | ✅ done 2026-10-25 |
| M3 | Operability | ✅ done 2026-10-25 |
| M4 | HTTP edge features (narrowed by ADR-0008) | ✅ done 2026-10-25 |
| M5 | Routing, filters and pools | ⚪ |
| M6 | WASM sandbox | ⚪ |
| M7 | Protocols: gRPC, WebSockets and SSE | ⚪ |
| M8 | Dynamic configuration | ⚪ |

kest is an application server behind a proxy, not an edge proxy (ADR-0008): a feature belongs to kest when it
concerns how the proxy talks to the application or makes up for a weakness of PHP.

---

## M0 — Foundation

**Goal:** the config → component graph → lifecycle pipeline works end to end, without workers yet.

Scope [K-001…K-015]:
- Repo skeleton (`cmd/kest`, `internal/…`), Makefile, golangci-lint v2, `make check` (no CI yet; K-003 deferred).
- CLI: `kest serve -c FILE`, `kest validate -c FILE [--dump]`, `kest version`.
- Config: YAML, strict decoding, env expansion, defaults, validation with YAML paths.
- Extension registry with typed categories.
- Server lifecycle: ordered start/stop, signals, graceful drain.
- `log/slog` setup; admin server with `/ready` and `/server_info`.
- HTTP listener, router (virtual hosts; `prefix` and `path` matches) and `direct_response`.

Exit criteria:
- `kest validate` rejects broken configs with path-aware errors (covered by tests).
- `kest serve -c examples/hello/kest.yaml` answers `curl localhost:8080/` with a direct response.
- SIGTERM drains in-flight requests and exits cleanly; no goroutine leaks.
- `make check` green locally: build, vet, lint, `test -race`.

Out of scope: worker processes, metrics, TLS.

## M1 — Worker pool and relay

**Goal:** a robust, language-agnostic process pool that speaks the RoadRunner worker protocol.

Scope [K-020…K-029]:
- Verified notes on the goridge frame format and the RR worker handshake/stop/error protocol.
- Frame codec (fuzzed), pipes relay.
- Worker process: spawn, handshake, exec, graceful stop, kill.
- Fixed-size pool: bounded queue, `allocate_timeout`, `max_jobs`, `destroy_timeout`.
- Supervisor: `ttl`, `idle_ttl`, `exec_ttl`, `max_worker_memory`; respawn with backoff.
- A fake worker in Go for tests; integration test with a PHP echo worker built on `roadrunner/worker`.

Exit criteria:
- The pool keeps N PHP echo workers serving under concurrent load and survives `kill -9` of random workers.
- Every limit has tests (with `testing/synctest`); `-race` and leak checks are clean.

Out of scope: HTTP mapping.

## M2 — PHP HTTP (MVP)

**Goal:** real PHP applications run behind kest.

Scope [K-040…K-048]:
- Verified notes on the RR HTTP payload format (request context, uploads, cookies, attributes, response).
- `php_workers` cluster type; HTTP request → payload, payload → HTTP response.
- `max_request_body`, multipart uploads, multi-value headers, remote address, TLS info.
- `examples/php-hello` (PSR-7 via `spiral/roadrunner-http`).
- Smoke test with a real framework (Symfony via a RoadRunner runtime ✅; Laravel Octane not tried).
- Getting-started docs; Docker-based e2e setup with a PHP 8.3 / 8.4 / 8.5 matrix, runnable locally.

Exit criteria:
- An unmodified `spiral/roadrunner-http` worker serves requests through kest.
- A real framework app boots and passes a smoke test.
- A basic load test (e.g. `oha` / `wrk`) runs without errors or leaks.

Out of scope: streaming responses, metrics (M3).

## M3 — Operability

**Goal:** you can run it in production and know what it is doing.

Scope [K-060…K-075]:
- Metrics (Q-E), admin `/config_dump`, `/clusters`, `/clusters/{name}/reset`, `/metrics`.
- Rolling worker reset (admin and SIGHUP) without failed requests.
- Filters: `access_log`, `request_id`, `recovery`; per-route timeouts; header and body limits.
- Benchmarks against RoadRunner (same worker, same machine), published in `docs/`.
- Operations guide.
- Worker errors that say what went wrong; replacing workers on code changes in development.
- Docs drift checks and a docs review before every milestone merge.

Exit criteria:
- Rolling reset under load completes with zero failed requests.
- The metrics are enough to build a useful dashboard.
- A benchmark report exists.

## M4 — HTTP edge features

Scope [K-080…K-096]: TLS from files, HTTP/2 (h2, h2c), static files (the `static` filter), gzip compression,
CORS, header manipulation, redirects, path normalization, trusted proxies, streaming responses from PHP, filters on
virtual hosts and routes. Plan: `plans/M4-http-edge.md`. Narrowed before the merge by ADR-0008: the `http_proxy`
cluster was built and removed, static files moved out of `php_workers` into a filter.

Exit criteria:
- HTTP/2 over TLS (and h2c) reaches a PHP app; renewed certificates are served without a restart.
- Prefix routes can't be bypassed with dot segments or duplicate slashes.
- PHP workers stream responses; a client that goes away stops the stream.
- A route with the `static` filter serves the files of `public/` without PHP; other routes and virtual hosts
  don't.

## M5 — Routing, filters and pools

**Goal:** the core that sets kest apart, done well: several PHP pools, routing between them, and filters that take
work off PHP before and after it.

Scope [K-140…, K-095, K-905, K-908, K-910]: route matches on method, headers, query and regular expressions; route
names and a `route` metrics label; counting the answers of listener filters; per-route filter settings (turn off or change a listener filter); weighted clusters;
`X-Sendfile`; fast rejection under overload; authentication (JWT, API keys), local rate limiting, external
authorization through a PHP cluster; priority queues and a dynamic size within a pool.

## M6 — WASM sandbox

Scope [K-100…]: an ADR on the guest ABI; wazero runtime with a compilation cache; instance pools; memory and
time limits; `wasm` cluster; `wasm` HTTP filter (on the filter model of M5); example modules (Go/TinyGo, Rust);
latency benchmarks. Possibly PHP → WASM calls through RPC.

## M7 — Protocols

**Goal:** the protocols a proxy speaks to an application server, and the connections PHP can't hold.

Scope [K-160…]: gRPC on the HTTP/2 listener (an ADR first; unary calls, then server streaming; kest's own errors as
gRPC statuses), WebSockets and SSE held by kest while PHP handles the events (an ADR first), unix socket listeners.

## M8 — Dynamic configuration

Scope [K-120…]: config reload on SIGHUP or file change, diff-based apply (restart only what changed),
draining old listeners, a config API. A design ADR first (xDS-inspired or not).

## Later / icebox

CI on GitHub Actions (K-003, deferred until a release or a second contributor), queues/jobs, KV, PHP → kest RPC,
systemd notify and socket activation, Envoy-style hot restart (passing FDs to a new process), a FastCGI listener
in place of PHP-FPM (K-911), embedding PHP via SAPI (would need to revisit ADR-0003).

---

## Definition of production-ready (1.0)

- Config schema `v1` frozen, with a written compatibility policy.
- Zero-downtime worker reloads and a documented binary-upgrade path.
- Crash-safe supervision; every resource bounded; sensible secure defaults (admin on localhost, size limits).
- Metrics, logs and admin API documented and stable.
- Fuzzed parsers; race-free, leak-free tests; integration tests in CI against every supported PHP version.
- Security review of the admin API and defaults.
- Docs: getting started, config reference, operations guide, migration notes from RoadRunner.
- Versioned releases with binaries, a Docker image and a changelog.

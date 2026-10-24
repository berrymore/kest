# Plan: M3 — Operability

- **Status:** Active (approved by the user 2026-10-25, D1–D10 included)
- **Milestone:** M3
- **Backlog:** K-060…K-075 (K-060, K-071, K-073 done before this plan)

## Goal

You can run kest in production and know what it is doing. `curl localhost:9901/metrics` shows requests,
latency, pool queues, workers and restarts, enough for a dashboard; `/clusters` shows every worker; a rolling
reset (`POST /clusters/{name}/reset` or SIGHUP) replaces the workers under load with zero failed requests, and a
reset to code that doesn't boot leaves the old workers serving; `access_log`, `request_id` and `recovery` are the
first HTTP filters; server timeouts and limits are configurable; buffered request bodies are bounded; a benchmark
against RoadRunner and an operations guide are in `docs/`.

## Design notes

Metrics follow ADR-0007. The user approved the plan with these decisions as a whole and left the details to the
agent (2026-10-25); the ones marked **schema** change the config file.

**D1. Metrics core.** `internal/metrics`, grown from the ADR-0007 prototype: registry, families with fixed label
names, counters, gauges (set, or read at scrape time), histograms with fixed buckets, the text format 0.0.4,
gzip. Components resolve their series when the graph is built. The status code is the only label that varies per
request; it indexes a fixed array (100–599), with no map lookup.

**D2. Metric set and names** (K-061). Prefix `kest_`. Routes have no names in the config, so HTTP metrics are
labeled by `listener`, `virtual_host` and `cluster` (empty for `direct_response`). A per-route label would need an
optional route `name` field: not in M3 unless the user wants it.

| Metric | Type | Labels |
|---|---|---|
| `kest_http_requests_total` | counter | `listener`, `virtual_host`, `cluster`, `code` |
| `kest_http_request_duration_seconds` | histogram, 5 ms…60 s | `listener`, `virtual_host`, `cluster` |
| `kest_http_requests_in_flight` | gauge | `listener` |
| `kest_pool_workers` | gauge | `cluster`, `state` |
| `kest_pool_queue_length` | gauge | `cluster` |
| `kest_pool_wait_seconds` | histogram | `cluster` |
| `kest_pool_rejected_total` | counter | `cluster`, `reason` (`queue_full`, `no_worker`) |
| `kest_pool_worker_restarts_total` | counter | `cluster`, `reason` (`max_jobs`, `ttl`, `idle_ttl`, `memory`, `exec_ttl`, `crash`, `stop_requested`, `start_failed`; `reset` with K-063) |
| `kest_pool_worker_memory_bytes` | gauge | `cluster`, `slot` |
| `go_*`, `process_*` | as in `client_golang` | a curated subset (ADR-0007) |

Workers are labeled by `slot`, never by `pid`, so the series don't grow as workers are replaced.

**D3. `/ready`** (K-062). 200 only while the server is ready **and** no cluster is `Unhealthy` (no live worker).
`Degraded` is normal for a moment whenever a worker is recycled, so it doesn't count.

**D4. Rolling reset with a surge** (K-063). One slot at a time: start the replacement process, wait for its
handshake, then retire the old worker after its current request. The pool runs at most `workers + 1` processes
during a reset and never below `workers` ready ones. If a replacement fails to boot, the reset stops and the old
workers keep serving: `POST /clusters/{name}/reset` returns the error, and SIGHUP logs it. A second reset while one
runs gets `409`. SIGHUP resets every cluster; reloading the config stays in M6.

**D5. Filters stay middleware** (Q-A). `func(http.Handler) http.Handler` is enough for `access_log`,
`request_id` and `recovery`; the phased model is decided when WASM filters arrive (M5).

**D6. `request_id`** (K-064, schema). Puts `X-Request-Id` on the request that reaches the cluster (PHP sees it)
and on the response, and makes it available to `access_log`. An incoming header is kept only with
`trust_incoming: true` (default `false`: the client can't choose the ID); otherwise kest generates 16 random bytes
as hex. Config: `{header: X-Request-Id, trust_incoming: false}`.

**D7. `access_log`** (K-064, schema). One line per request through kest's own logger (stderr, `log.format`), with
`component=access`: method, path, status, bytes, duration, remote address, request ID, virtual host, cluster. No
files and no rotation in M3: containers and systemd collect stderr. Config: `{}` for now.

**D8. `recovery`** (K-064). A panic below the filter answers `500` (if nothing was written yet) and logs the stack;
`net/http` alone would drop the connection.

**D9. Server limits and drain delay** (K-065, K-068, schema). Per listener, under `http`: `read_header_timeout`
(10s), `read_timeout` (off), `write_timeout` (off), `idle_timeout` (120s), `max_header_bytes` (1MB); the defaults
are today's fixed values. `server.drain_delay` (default `0s`): after SIGTERM, `/ready` turns `503` at once, and
the listeners keep serving for this long before they start to drain.

**D10. Buffered bodies** (K-070, schema). A per-cluster budget, `php_workers.max_buffered_bodies` (default
`256MB`): a request reserves its `Content-Length` (or `max_request_body` when the length is unknown) before its
body is read, and gets `503` when the budget is full. This bounds the memory regardless of the queue size.

**Other choices.**
- `/clusters` (K-074): JSON with each cluster's type, health and workers (slot, state, pid, jobs, age, memory),
  from an optional interface that a cluster implements; `php_workers` maps `Pool.Workers()`.
- `/config_dump` (K-075): the effective config as YAML, like `kest validate -dump`, with every `env` value of a
  cluster replaced by `[redacted]`, since the admin API has no authentication. `validate -dump` stays unredacted.
- Benchmarks (K-066): the same `examples/php-hello` worker behind kest and behind RoadRunner (the release binary,
  for the benchmark only, not a dependency), on the same machine, through a small load generator in Go in
  `test/bench` (no dependency; `ab` is single-threaded). The report goes to `docs/benchmarks.md`. K-069 is done
  only if the profiles show the body copy.
- Replacing workers on file changes (K-072, schema): polling with `os.Stat` (no fsnotify dependency) over
  configured directories and patterns, bounded in files and frequency, debounced, then a rolling reset (D4).
  Config: `php_workers.reload: {dirs: [src, config], patterns: ["*.php"], interval: 1s}`. Off by default.

## Steps

The first steps are the vertical slice: real metrics from a PHP app at `/metrics`, then state and resets.

1. [x] **Metrics core and instrumentation** (K-061) — `internal/metrics`, output checked with `expfmt` (test-only,
   ADR-0007); HTTP metrics in the router, pool metrics from the pool, Go and process metrics. Acceptance: a fuzzed
   set of series round-trips through `expfmt`; unit tests see the HTTP and pool series move.
2. [x] **`/metrics` and `/ready`** (K-062) — `GET /metrics` on the admin server, `/ready` per D3. Acceptance: e2e
   with `examples/php-hello`: the counters and histograms move as requests are sent.
3. [x] **`/clusters`** (K-074) — acceptance: e2e shows every worker of `php-hello` with pid, state and jobs.
4. [x] **Rolling reset** (K-063) — `Pool.Reset`, `POST /clusters/{name}/reset`, SIGHUP. Acceptance: a PHP e2e test
   resets under load (all workers busy) with zero failed requests; a reset to a worker that fails to boot leaves
   the old ones serving; `kest_pool_worker_restarts_total{reason="reset"}` counts it.
5. [x] **Filters** (K-064) — `request_id`, `access_log`, `recovery` in `internal/filters/<name>/`, documented in
   `docs/config.md`. Acceptance: e2e sees the ID in PHP's `$_SERVER`, in the response and in the access log.
6. [ ] **`/config_dump`** (K-075) — acceptance: env values never appear in the output.
7. [ ] **Server limits** (K-065) and **drain delay** (K-068) — acceptance: e2e for a slow client (read header
   timeout) and for `/ready` turning 503 during the delay while requests still succeed.
8. [ ] **Buffered body budget** (K-070) — acceptance: concurrent large uploads past the budget get `503`, and
   memory stays under the budget.
9. [ ] **Benchmarks** (K-066, maybe K-069) — `docs/benchmarks.md` with the method and the numbers.
10. [ ] **Operations guide** (K-067) — `docs/operations.md`: signals, rolling resets, metrics with example queries,
    probes, sizing, a systemd unit.
11. [ ] **Replace workers on file changes** (K-072).
12. [ ] **Docs review** (`review.md`), then the PR.

## Risks / open questions

- The surge (D4) needs a slot to own two processes for a moment; the pool's per-slot goroutine model has to
  allow that without a race. If it gets too complex, the fallback is "stop, then start" per slot (capacity
  `workers - 1` during a reset), which still loses no request while the queue absorbs the gap.
- Without route names, two routes to the same cluster on one virtual host can't be told apart in metrics.
- RoadRunner's binary for K-066 has to be downloaded; its version goes into the report.

## Log

- 2026-10-25 — Step 5 (K-064) done as in D5–D8. The router and the listener entry fill `httpx.RequestInfo`, so
  `access_log` gets the virtual host and cluster in any filter order; a panic counts as a 500 in the HTTP metrics;
  `access_log` logs the path without the query string. `request_id` changes a copy of the request.
- 2026-10-25 — Step 4 (K-063) done, with the surge of D4. A slot takes a handed-over process only between jobs; a
  slot whose process started after the reset began (generation counter, not timestamps) drops it. Under load,
  `max_jobs` replacements make some reset handovers unnecessary, so the reset count can be below the worker count.
  `POST /clusters/{name}/reset` blocks until done and ignores a client that disconnects.
- 2026-10-25 — Step 3 (K-074) done.
- 2026-10-25 — Step 2 (K-062) done: the e2e tests read `/metrics` from `examples/hello` and `examples/php-hello`.
- 2026-10-25 — Step 1 (K-061) done. Deviations: restarts also count `start_failed`; `kest_pool_rejected_total`
  and `kest_pool_wait_seconds` as planned; `process_resident_memory_bytes` exists on Linux only (macOS needs cgo);
  pool log lines now say `reason=memory` / `reason=stop_requested`, the metric label values. The fuzz test found
  that Prometheus's parser drops blanks before a help text: kest's help texts have none.
- 2026-10-25 — Approved by the user ("in general I approve; you know better how to do it"). Steps 1 and 2
  regrouped so that each is one backlog item.
- 2026-10-25 — Draft. K-062 split: `/clusters` is K-074, `/config_dump` is K-075; `POST /clusters/{name}/reset`
  belongs to K-063.

# Plan: M5 — Worker pools and routing to them

- **Status:** Active. Approved by the user on 2026-10-25 as a whole, the **schema** decisions included (they change
  the config file or the metrics); D4 brings its own names and defaults to the user after its measurement.
- **Milestone:** M5
- **Backlog:** K-151, K-905, K-144, K-908, K-143, K-140, K-910, K-095, K-149, K-152

## Goal

A pool of PHP workers serves a whole application without one part of it taking the rest down. A slow endpoint gets
at most a few workers of the pool and waits in its own queue; important routes go first when the pool is
overloaded, and unimportant ones are turned away fast with `Retry-After` instead of waiting until the client gives
up; the pool grows under load and shrinks when it ends; PHP hands large files to kest with `X-Sendfile` and is free
at once. Routes can tell requests apart by method, headers, query and regular expressions, and every metric of a
request and of its wait in the pool names its route.

Scope, agreed with the user on 2026-10-25: the pool is the focus. Weighted clusters (K-142) are dropped: one kest
runs one release of one application, so a split between releases belongs to the proxy or the orchestrator, and an
A/B split needs a sticky assignment that PHP does itself. Authentication (K-145), rate limiting (K-146) and external
authorization (K-147) wait in the icebox; per-route filter settings (K-141) move to M6, where the WASM filter
extends the filter model anyway.

Exit criteria:
- A slow route limited to 2 of a pool's 4 PHP workers and flooded with requests doesn't slow the other routes of the
  pool: their p99 latency stays close to what it is under the same load without the flood, and
  `kest_pool_wait_seconds{route=…}` shows the slow route waiting (step 5).
- Under overload, high-priority routes are served first and low-priority requests get `503` with `Retry-After`
  within their `queue_timeout`.
- A pool with `max_workers` above `workers` grows while requests wait and shrinks back after `scale_down_after`.
- PHP answering `X-Sendfile` makes kest send the file, with ranges and conditional requests; a path outside the
  configured directories is refused.
- Routes match on method, headers, query parameters and RE2 paths.
- `make bench` shows no regression against the K-148 numbers; K-149's profile and its result are recorded.
- `make check`, `make test-integration` and `make test-matrix` are green.

## Design notes

**D1. Route names** (K-910, schema). `routes[].name`, optional, unique in the whole config (the pool labels its
lanes by the route name alone, D2). `RequestInfo.Route` carries it. `kest_http_requests_total` and
`kest_http_request_duration_seconds` get a `route` label (empty for unnamed routes and for requests that match
none); `access_log` writes `route`.

**D2. Lanes: per-route scheduling within a pool** (K-151, K-905, schema). Three optional fields on the `route`
action:

```yaml
- name: export
  match: { prefix: /export/ }
  route:
    cluster: app
    timeout: 120s
    max_workers: 2       # at most 2 of the cluster's workers serve this route at once; more requests wait
    priority: low        # high | normal (default) | low
    queue_timeout: 10s   # the longest wait for a worker; default: the cluster's pool.allocate_timeout
```

- A route with any of them gets its own **lane** in the cluster's pool, and must have a `name`. Routes without them
  share the pool's default lane. Each lane is a FIFO queue bounded by `pool.max_queue_size`, so a flood on one lane
  doesn't fill the queue of the others.
- A free worker takes the oldest request of the highest-priority lane that is under its `max_workers`. Priority is
  strict: under sustained overload low lanes don't get workers, and their `queue_timeout` turns the wait into a fast
  `503`. That is load shedding by priority, on purpose; no weighted fairness.
- A request counts against its lane's `max_workers` until the worker is done with it, the last frame of a stream
  included.
- `max_workers` is between 1 and the pool's largest size (`pool.max_workers`, D5). The router asks the cluster for a
  handler per route with these options (an optional interface in `internal/cluster`); a cluster type that doesn't
  support them, or a value it can't honor, is a config error at the route's YAML path.
- `worker.Pool` gets lanes, made before `Start`: `Pool.Lane(LaneConfig)`, and `Lane.Exec`/`ExecStream`; the pool's
  own methods use the default lane. The unbuffered jobs channel gives way to a dispatcher under one mutex: per-lane
  queues and a list of idle slots. A slot that becomes ready takes the next eligible job or parks on its own
  channel; `Exec` hands its job to a parked slot directly when its lane is under its limit. A slot that leaves the
  idle list (timer, memory, reset, stop) while a job is being handed to it gives the job back to the head of its
  lane.
- Metrics: `kest_pool_wait_seconds`, `kest_pool_queue_length` and `kest_pool_rejected_total` get a `route` label
  (empty for the default lane); new `kest_pool_lane_workers{cluster, route}`, the workers serving each lane now.
  `/clusters` lists the lanes with their settings, queue and busy workers.
- Known limit: lanes share the cluster's `max_buffered_bodies`, so a flood of uploads on one lane can still turn
  away the bodies of others; the docs say so.

**D3. `Retry-After` and per-route queue timeout** (K-144, schema). Every `503` the pool answers (`queue_full`,
`no_worker`, `body_budget`, `overload`) carries `Retry-After`, from `php_workers.retry_after` (default `1s`; `0s`
leaves it out). A lane's `queue_timeout` replaces `allocate_timeout` for its requests (D2).

**D4. Adaptive queue timeout under overload** (K-144, schema after the experiment). The CoDel scheme as Facebook
describes it for RPC queues ("Fail at scale", 2015 ❓): a lane whose queue hasn't been empty during the last
`interval` is overloaded, and its new requests wait at most `target` instead of their `queue_timeout`. A standing
queue then turns into fast `503`s (`reason="overload"`), and the requests that get in reach a worker quickly instead
of waiting most of their timeout. Step 6 measures it first: `test/bench` at about twice the pool's capacity, with
and without, comparing the goodput and the p50/p99 latency of successful requests. The measurement decides the
names (candidate: `pool.overload: {target, interval}`), the defaults, and whether it is on by default; they go to
the user before the code lands.

_Decided in step 6 (the user had delegated it; for the PR review):_ `pool.overload: {queue_timeout: 500ms,
interval: 5s}`, **on by default**, `queue_timeout: 0s` turns it off. "Overloaded" means requests have waited in the
lane without a break for `interval`; only a worker taking the last waiting job ends that (not timeouts or shedding,
which would make it flap). In an overloaded lane new requests wait at most `queue_timeout`, and a free worker sheds
the heads that waited longer (`503`, reason `overload`, `worker.ErrOverloaded`). Lanes with `max_workers` or a
`queue_timeout` of their own are left out. Measurements in `docs/benchmarks.md` § Overload control.

**D5. Dynamic pool size** (K-908, schema).

```yaml
pool:
  workers: 4             # always running
  max_workers: 16        # under load, up to this many in total; default: workers (a fixed size)
  scale_down_after: 1m   # an extra worker idle this long stops
```

- RoadRunner (`pool/v2` v2.0.0 ✅) adds workers only after a request has waited the whole `allocate_timeout` (60s by
  default) without one. kest starts an extra worker as soon as a request that its lane would let through finds no
  free worker; a request held back by its lane's `max_workers` doesn't count.
- Boots are CPU-heavy and compete with the serving workers, so at most `max(1, workers/4)` extra workers boot at
  once (adjusted in step 7 if the measurement says so, with a log entry).
- The extra slots exist from `New`, each with its goroutine as today, parked without a process (a new slot state,
  `parked`). `Health` counts the base `workers`; a reset replaces the extra workers that run. An extra worker that
  reaches a limit (`max_jobs`, `ttl`, memory) parks instead of being replaced when nothing waits;
  `scale_down_after` takes the place of `supervisor.idle_ttl` for extra workers.

**D6. `sendfile` filter** (K-143, schema).

```yaml
filters:
  - type: sendfile
    config:
      dirs: [storage/app]    # required: files outside these directories are refused; relative to the config file
```

- A `200` from the route's cluster with `X-Sendfile: <path>`: kest removes that header and the body, and sends the
  file with `http.ServeContent` (`Content-Length`, ranges, `Last-Modified`, `If-Modified-Since`, `If-Range`),
  keeping the application's `Content-Type`, `Content-Disposition` and `Cache-Control`. The path must resolve, symlinks
  included, inside one of `dirs`; a missing file is `404`, a path outside `dirs` `403`, both logged as errors of the
  application.
- The filter sets `X-Sendfile-Type: X-Sendfile` on requests, replacing one a client sent. Symfony's
  `BinaryFileResponse` (behind Laravel's `download()` and `file()` too) then answers with the header and no body,
  once the application calls `BinaryFileResponse::trustXSendfileTypeHeader()` (Symfony 8.1.9 ✅). It keeps
  `Content-Length` (the file size) and doesn't handle ranges itself, which is why kest must.
- RoadRunner's `sendfile` middleware (`send` v6.0.0 ✅) takes any path without `..`, has no directory limit, always
  sends `application/octet-stream` with status `200`, and supports no ranges. kest reads the same header and differs
  on the rest on purpose. `X-Accel-Redirect` names a URI of an nginx location, not a file: it passes through for
  the nginx in front.
- PHP doesn't read the file, so `max_response_size` doesn't apply and memory stays flat.

**D7. Route matching** (K-140, schema).

```yaml
match:
  regex: ^/(export|reports)/    # RE2, matched against the whole path; or prefix, or path
  methods: [GET, HEAD]          # any of them; default any method
  headers:                      # all must match
    - name: X-Requested-With
      exact: XMLHttpRequest     # exactly one of: exact, prefix, regex, present (true or false)
  query:                        # all must match
    - name: format
      exact: csv
```

- `regex` must match the whole path, as Envoy's `safe_regex` (v1.40.1 ✅). Header names are case-insensitive, values
  case-sensitive; a header sent several times is matched as its values joined with `", "`. A query parameter sent
  several times is matched by its first value, as in Envoy ✅. `present: false` matches a missing header or
  parameter.
- No suffix or contains matchers, no inversion beyond `present: false`, no runtime fractions: `regex` covers the
  rest.
- `redirect.prefix_rewrite` still needs `match.prefix`.

**D8. Requests that listener filters answer** (K-095, no schema). They are counted in the HTTP metrics with an empty
virtual host, route and cluster, like requests that match no virtual host.

**D9. CPU per request** (K-149, no schema). Profiled after the lanes (step 11), so the profile covers the new
dispatcher; the form path (`rrhttp` form tree, `encoding/json`) and what M4 added per request first. Step 3 already
compares `make bench` with the K-148 numbers, so the dispatcher can't hide a regression until then.

**D10. Fork-based worker start** (K-152, research only). A framework boots in 0.1–1 s, which makes the pool slow to
grow (D5) and every reset slow. A booted PHP process that forks its workers would start them in milliseconds and
share their memory copy on write. Questions: the children can't share the parent's pipes, so they need socket relays
(K-029); the application's worker script needs a hook before `Worker::create()`; connections opened during the boot
(database, Redis) must be reopened in the children, and `mt_rand` reseeded. Step 12 measures boot against fork time
and memory on the Symfony smoke application and writes a research note, plus an ADR draft if it is worth building.
No code in M5.

## Steps

The vertical slice first (steps 2–5): two routes on one pool, one of them slow and limited, with per-route metrics,
end to end. The user can look at it before the rest.

1. [x] Plan, roadmap and backlog updated; research notes: RoadRunner's `dynamic_allocator` and `send`, Envoy's route
   matching, Symfony's `X-Sendfile` (this change) — acceptance: the user approves the plan.
2. [x] K-910 route names and the `route` label — acceptance: two named routes to one cluster show as two series;
   names are validated unique; `access_log` writes the route; `docs/config.md` and `docs/metrics.md` updated.
3. [x] K-151 lanes in `worker.Pool` (D2): dispatcher, per-lane queues, `max_workers`, priority, queue timeout;
   synctest tests for ordering, limits, timeouts, the hand-over race and stop; the existing pool tests unchanged
   and green — acceptance: `go test -race ./internal/worker/...`; a Go benchmark of `Exec` and `make bench` no
   slower than before (K-148 numbers).
4. [x] K-151, K-905, K-144 (D2, D3) route options to lanes: schema, validation at the route's path, `php_workers`
   lanes, `Retry-After`, lane metrics, `/clusters` — acceptance: unit tests and an e2e test with fake workers.
5. [x] The slice end to end — acceptance: an integration test with PHP floods a slow route limited to 2 of 4
   workers and checks that a fast route's latency stays low (exit criterion 1); `docs/config.md`, `docs/metrics.md`,
   and an operations guide section "Isolating slow endpoints".
6. [x] K-144 overload (D4): the experiment, then the proposal (names, defaults) to the user, then the code —
   acceptance: the numbers are in `docs/benchmarks.md` and the log; tests with synctest.
7. [ ] K-908 dynamic pool size (D5) — acceptance: synctest tests for growing, the boot limit, shrinking and parking;
   an integration test with PHP; docs.
8. [ ] K-143 `sendfile` filter (D6) — acceptance: unit tests (ranges, conditional requests, symlinks out of `dirs`,
   missing files, a client's own `X-Sendfile-Type`); the Symfony smoke test downloads a file through
   `BinaryFileResponse`; docs.
9. [ ] K-140 route matching (D7) — acceptance: router tests for every matcher; validation errors located (a bad
   regex shows RE2's message); docs.
10. [ ] K-095 requests answered by listener filters in the metrics (D8) — acceptance: a CORS preflight on a listener
    filter is counted.
11. [ ] K-149 profile and trim kest's CPU per request (D9) — acceptance: the profile and the before/after numbers in
    `docs/benchmarks.md`; whatever is changed, `make bench` doesn't get slower.
12. [ ] K-152 fork-based start, research (D10) — acceptance: a note in `research/`, an ADR draft if it is worth it.
13. [ ] Docs review (`review.md`), `architecture.md` (§ 4 lanes, § 5 slot states, § 8 metrics, Q-F), then the PR.

## Risks / open questions

- The dispatcher replaces the core of the pool, its most tested code. Mitigation: the pool's tests stay as they are,
  synctest for the new timing, `-race`, the Go benchmark and `make bench` in step 3.
- Strict priority starves low lanes under sustained overload. Intended (D2), but the docs must say it plainly.
- CoDel's parameters may not suit PHP's latencies (tens to hundreds of milliseconds per request, against Facebook's
  milliseconds); D4 may end up opt-in.
- Extra workers booting under load take CPU from the serving ones (D5): the boot limit and step 7's measurement.
- Lanes share the body budget (D2).

## Log

- 2026-10-25 — Plan written after the scope talk with the user (see Goal). Verified for it: RoadRunner `pool/v2`
  v2.0.0 `dyn_allocator.go` and `send` v6.0.0 `plugin.go`; Envoy v1.40.1 `route_components.proto`; Symfony
  `http-foundation` 8.1.9 `BinaryFileResponse::prepare()` (the vendor directory of `playground/symfony`). Notes in
  `research/roadrunner.md` and `research/envoy.md`.
- 2026-10-25 — The user approved the plan (Q14) and the `docs:` commit; step 2 started.
- 2026-10-25 — Step 2 (K-910): a named route labels every request it serves, also those its `direct_response`,
  `redirect` or filters answer (`static`); the router marks the route when it is chosen (`chosen`) and the cluster
  when it is reached (`routed`). `examples/php-hello` names its routes; its e2e test checks the series.
- 2026-10-25 — Step 3 (K-151, K-905 in the pool): `internal/worker/lane.go`. The existing pool tests pass unchanged
  (two read `p.waiting`, which stays); new synctest tests for limits, priority, queue timeouts and per-lane queues,
  and a step-by-step test of the hand-over (checked by breaking the code: each of four mutations fails a test).
  A job handed to a parked slot that leaves for another event goes back to the head of its lane; its caller may have
  seen its queue timeout meanwhile, so the deadline is checked when it goes back. `Observer.Waited` and `Rejected`
  take the lane. New `BenchmarkPoolExec` (8 fake workers, 48 callers): 3.1 µs → 1.8 µs per job, 21 allocations as
  before, 904 → 985 bytes. `make bench`-style A/B of the binaries before and after (hello and form, 16 and 64
  clients, 6 interleaved runs each, `json`, no RoadRunner): medians −2.1 % to +1.9 %, the ranges overlap: no
  measurable difference.
- 2026-10-25 — The user delegated the rest of M5 ("делай все шаги"); push and PR wait for their OK.
- 2026-10-25 — Step 4: `route.max_workers`, `priority`, `queue_timeout`; `httpx.ClusterLookup(cluster, *Lane)` and
  `cluster.Laner`; `php_workers.retry_after` (1s); lane metrics and `/clusters` lanes. Deviation: no e2e test with
  fake workers. The fake worker echoes the request instead of answering in the RoadRunner HTTP format, so it can't
  serve through `php_workers`; the router, server and cluster unit tests cover the wiring, and step 5's PHP test runs
  it end to end through the binary. `config.RouteAction` now carries its YAML path (`Errorf`), like `Extension`, so
  that what a cluster finds wrong with a lane is located at the route.
- 2026-10-25 — Step 5: `test/e2e/php_lanes_test.go` (PHP, through the binary): 4 workers, 8 clients on a route that
  sleeps 500 ms, 2 clients on another route. With `max_workers: 2, priority: low` on the slow route the other
  route's p99 is 0.46–0.51 ms; without the lane 0.8–1.0 s. The vertical slice is done; operations guide section
  "Isolating slow endpoints".
- 2026-10-25 — Step 4 was committed after `make check` alone, and two PHP tests (`TestPHPMetrics`,
  `TestServePHPHelloExample`) still expected the pool metrics without `route`; fixed in step 5's commit. From here
  on, steps that touch `php_workers` or the e2e tests run `make test-integration` before their commit too.
- 2026-10-25 — Step 6 (K-144, D4). `test/bench/loadgen` can't overload a server with a fixed number of clients, so
  the experiment ran in virtual time first (`internal/worker/overload_experiment_test.go`, skipped unless
  `KEST_EXPERIMENT=1`: Poisson arrivals, exponential service of 20 ms and 100 ms, clients giving up after 5 s), then
  with PHP through a new `loadgen -rate` (open loop). Findings: without overload control, overload collapses (100 ms
  requests at 200 %: 7 of 40 requests/s served); 100ms/1s answers fastest but turns away 2–5 % at 95 % load;
  500ms/5s never acted below capacity in any run. The first version shortened only the wait of new arrivals and
  reset on any empty queue: with PHP its p90 stayed at 3.9 s (the backlog from before the overload was seen still
  waited) and it could flap; shedding stale heads at dispatch and ending a backlog only when a worker takes the
  last job brought p90 to 1.7 s and the clients that gave up to 0. Lanes with `max_workers` are left out too (their
  queue is the limit at work, not an overload). The pool's unit tests run without overload control
  (`testConfig`); `TestOverload` and `TestOverloadBacklog` test it.

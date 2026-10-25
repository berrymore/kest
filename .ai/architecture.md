# Architecture

> A living document. It describes the **target** design and marks what is implemented.
> Legend: 🟢 implemented · 🟡 in progress · ⚪ planned.
> **Last reviewed: 2026-10-25 (M4 done, narrowed by ADR-0008).** Implemented: config loading and validation, extension
> registry, server lifecycle, HTTP listeners with configurable limits, TLS (reloaded certificates) and HTTP/2/h2c,
> path normalization, router with `direct_response` and `redirect`; the goridge relay and the worker pool with
> rolling resets and streamed answers (`internal/relay`, `internal/worker`); the RoadRunner HTTP payload
> (`internal/rrhttp`); the cluster type `php_workers` (body budget, `reload`); the filters `request_id`,
> `access_log`, `recovery`, `client_ip`, `headers`, `cors`, `compression`, `static`, on listeners, virtual hosts and
> routes;
> metrics (`internal/metrics`); admin `/ready`, `/server_info`, `/config_dump`, `/clusters`,
> `POST /clusters/{name}/reset`, `/metrics`.

## 1. Big picture 🟡

```
                ┌──────────────────────────────── kest ─────────────────────────────────┐
                │                                                                       │
  proxy ──────▶ │  Listener ──▶ HTTP connection manager                                 │
                │               ├─ filter: request_id                                   │
                │               ├─ filter: access_log                                   │
                │               ├─ filter: …                                            │
                │               └─ router (implicit, last)                              │
                │                    │ vhost by Host → its filters                      │
                │                    │ first matching route → its filters               │
                │                    ├─ direct_response, redirect                       │
                │                    └─ route → Cluster                                 │
                │                               ├─ php_workers ─▶ Pool ─▶ Relay ────────┼──▶ php worker.php × N
                │                               └─ wasm (M6)  ─▶ wazero instances       │
                │                                                                       │
                │  Admin server (127.0.0.1:9901): /ready /server_info /config_dump        │
                │                                 /clusters /metrics                     │
                └───────────────────────────────────────────────────────────────────────┘
```

kest is a single Go binary. At startup it reads one YAML file and builds a graph of components:

- **Listeners** accept connections on an address and hand them to a protocol handler (HTTP first).
- The **HTTP connection manager** runs a chain of **HTTP filters** (middleware), ending in the **router**.
- The **router** selects a **virtual host** by `Host` and runs its filters, then the first matching **route**,
  and runs its filters and its action: forward to a **cluster**, return a **direct response** or a **redirect**. Paths are normalized before
  the filters (`normalize_path`, `merge_slashes`).
- **Clusters** are named upstreams of different types (`php_workers`; `wasm` later).
- **Listeners** may terminate TLS (certificates by SNI, reloaded when their files change) and speak HTTP/2.
- A `php_workers` cluster owns a **pool** of processes, talks to each one through a **relay**
  using a framed protocol (ADR-0003), and is watched by a **supervisor**.
- The **admin server** exposes readiness, effective config, cluster and worker state, and metrics.

## 2. Request flow: HTTP → PHP 🟢

1. The listener accepts a connection; the `net/http` server parses the request.
2. HTTP filters run in config order (e.g. `request_id` → `access_log` → `cors`).
3. The router picks a virtual host by `Host` and runs its filters, then the first route whose `match` fits and runs
   its filters. A filter may answer itself; then the request stops there.
4. Action `route: {cluster: php_app, timeout: 30s}` → the request context gets the route timeout.
5. The `php_workers` cluster encodes the request into a **payload** (`rrhttp.NewRequest`): `context` =
   request head (method, absolute URI, headers, cookies, remote IP, …) as JSON or protobuf (`codec`);
   `body` = the raw body or, for forms, a JSON tree of fields in PHP's bracket syntax; bounded by
   `max_request_body`. Before reading it, the request reserves its size in the cluster's `max_buffered_bodies`
   budget (`503` when full), and the read gets a deadline from the route timeout (`408` for a slow sender).
6. The **pool** takes a free worker, waiting at most `allocate_timeout` in a bounded queue, and sends
   the payload over the relay.
7. The PHP worker (`$worker->waitRequest()` … `$worker->respond()`) answers; kest decodes status,
   headers and body (`rrhttp.DecodeResponse`) and writes the response with its own framing
   (`rrhttp.WriteResponse`). Pool and worker errors become 5xx (`rrhttp.ErrorStatus`). A streamed answer is
   written frame by frame (`rrhttp.DecodeStreamFrame`, `WriteInformational`, `WriteStreamHeader`), flushed.
8. The worker goes back to the pool. The supervisor checks its limits (max_jobs, memory, TTL) and
   recycles it if needed.

## 3. Configuration model 🟢

Decided in ADR-0002. Full example; every field in it exists (`docs/config.md` has them all):

```yaml
version: v1

log:
  level: info                # debug | info | warn | error
  format: json               # text | json

admin:
  address: 127.0.0.1:9901

listeners:
  - name: public
    address: 0.0.0.0:8080
    http:
      filters:               # run in order; the router is implicit and always last
        - type: request_id
        - type: access_log
      virtual_hosts:
        - name: app
          domains: ["example.com", "*.example.com"]
          routes:
            - match: { prefix: /healthz }
              direct_response: { status: 200, body: "ok" }
            - match: { prefix: /reports/ }
              route: { cluster: php_heavy, timeout: 120s }
            - match: { prefix: / }
              filters:       # after the router chose this route: files of public/ without PHP
                - type: static
                  config: { dir: public }
              route: { cluster: php_app, timeout: 30s }

clusters:
  - name: php_app
    type: php_workers
    config:
      command: [php, public/worker.php]   # dir defaults to the config file's directory
      env: { APP_ENV: prod }               # added to kest's environment (inherit_env: true)
      codec: json                          # json | proto
      max_request_body: 16MB
      pool:
        workers: 8
        max_jobs: 1000
        allocate_timeout: 60s
        destroy_timeout: 30s
      supervisor:
        ttl: 1h
        idle_ttl: 10m
        exec_ttl: 60s
        max_worker_memory: 256MB

  - name: php_heavy          # a separate pool isolates slow endpoints
    type: php_workers
    config:
      command: [php, public/worker.php]
      pool: { workers: 2 }
```

Rules:

- One file, with a mandatory `version: v1`.
- Strict decoding: unknown fields are errors.
- **Typed extensions** use `{type: <name>, config: {...}}`. The type is looked up in the registry of the
  category where it appears (HTTP filters, clusters, …); `config` is decoded strictly into that
  extension's config struct.
- Names are unique per kind. References by name (route → cluster) are validated before startup.
- `${VAR}` and `${VAR:-default}` are expanded in **scalar values after parsing**, not in the raw text,
  so env values can't inject YAML structure. A plain scalar is re-typed after expansion (`${PORT}` → int);
  a quoted one stays a string, and the type error says so.
- Implementation (`internal/config`): parse into a `yaml.Node` tree → walk it once to expand env and index
  `path ↔ line:column` → strict decode (`WithKnownFields`) → defaults → validation. Errors are
  `config.Errors`, each `file:line:col: path: message`, all reported at once. Extension `config` blocks
  stay as raw nodes (`config.Extension`) and are decoded by `registry.Build` against the same index, so
  their errors are located too.
- The effective config (defaults applied) is available through `kest validate -dump`, which is local and shows
  everything, and admin `/config_dump`, which redacts what extension configs mark through `config.Redactor`
  (php_workers: `env` values).

## 4. Core abstractions

Implemented 🟢 (see the packages for the full contracts):

```go
// Every long-lived component (listeners, clusters, admin) has this lifecycle.
// Start returns once the component is ready; long-running work runs in
// goroutines it owns. Stop is graceful within the ctx deadline.
Start(ctx context.Context) error
Stop(ctx context.Context) error

// internal/registry (ADR-0004): one Category per extension point, filled from init().
type Factory[T any] struct {
	Type      string                                    // "php_workers", "access_log", …
	NewConfig func() any                                // pointer to a config struct pre-filled with defaults; may implement Validate() error
	Build     func(bc BuildContext, cfg any) (T, error) // no goroutines, no side effects; may read files the config names
}
type BuildContext struct {
	Name      string // component name (clusters)
	Logger    *slog.Logger
	ConfigDir string            // absolute directory of the config file: relative paths resolve against it
	Metrics   *metrics.Registry // the server's; MetricsRegistry() returns a throwaway one when nil (tests)
}
func Build[T any](c *Category[T], ext *config.Extension, bc BuildContext) (T, error)

// internal/httpx: filters are plain middleware for now (open question Q-A).
type Filter func(next http.Handler) http.Handler
var Filters = registry.NewCategory[Filter]("http filter")
// Every request carries an *httpx.RequestInfo (RequestID, VirtualHost, Cluster), set by the listener entry,
// the router and request_id; filters read it after the inner handler returns (access_log), whatever their
// order. httpx.ResponseWriter records the status and body size; httpx.BeforeHeader edits a response header just
// before it goes out (headers, cors, compression). RequestInfo.ClientIP/Scheme come from client_ip
// (httpx.ClientIP, httpx.Scheme). Built in: request_id, access_log, recovery, client_ip, headers, cors, compression.

// internal/cluster: the name lives in config/BuildContext, not on the interface.
type Cluster interface {
	http.Handler // must honor the request context; answers 504 on route timeout (Q-D)
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}
var Types = registry.NewCategory[Cluster]("cluster")
// Optional: Prober { Serving() bool }; /ready fails while a cluster can't serve (php_workers: no live worker).
// Optional: Reporter { Report() any }; its JSON is the cluster's status in /clusters.
// Optional: Resetter { Reset(ctx) error }; POST /clusters/{name}/reset and SIGHUP (ErrResetRunning).
```

Worker pool and relay 🟢 (M1; used by the `php_workers` cluster from M2):

```go
// internal/worker: language-agnostic, knows nothing about HTTP or PHP.
type Payload struct {
	Codec   relay.Flags // how Context is encoded: relay.CodecRaw, CodecJSON, CodecProto
	Context []byte      // metadata, e.g. the encoded HTTP request head
	Body    []byte
}
func DefaultConfig() Config                       // explicit defaults; zero means "off" where allowed
func New(cfg Config, logger *slog.Logger) (*Pool, error)
func (p *Pool) Start(ctx context.Context) error   // all workers handshaked, or an error
func (p *Pool) Stop(ctx context.Context) error    // jobs finish, stop frames, kill at the deadline
func (p *Pool) Exec(ctx context.Context, req Payload) (Payload, error)          // a stream joined, up to MaxFrameSize
func (p *Pool) ExecStream(ctx context.Context, req Payload) (Payload, *Stream, error)
// A streamed answer (STREAM bit): the first frame, then Stream.Next(ctx) (Payload, ok, error) until ok is false;
// Stream.Close sends STOP and drops the rest. The slot goroutine answers PING with PONG; ExecTTL bounds each frame.
func (p *Pool) Workers() []Info                   // per slot: state, pid, jobs, started, memory
func (p *Pool) Health() Health                    // healthy / degraded / unhealthy
func (p *Pool) Waiting() int                      // callers waiting for a free worker
func (p *Pool) Reset(ctx context.Context) error   // rolling replacement, one slot at a time; ErrResetRunning
// Config.Observer (optional) hears Waited(d), Rejected(reason) and Replaced(reason); reasons are metric labels.
// Errors: ErrQueueFull, ErrNoWorker, ErrStopped, ErrStopRequested, ErrStreamTooLarge (Exec only),
// *JobError (soft, worker kept),
// *ProcessError (worker gone; wraps ErrExecTimeout or ErrStartTimeout on timeouts, *StdoutError
// with the start of the output when the worker printed to stdout outside the protocol).

// internal/relay: goridge framing, verified against goridge v4 and roadrunner/goridge.
type Frame struct {
	Flags   Flags  // FlagControl, FlagError, codec bits
	Stream  Stream // byte 10: StreamMore, StreamStop, StreamPing, StreamPong
	Options []uint32
	Payload []byte
}
func NewConn(r io.Reader, w io.Writer, maxPayload int) *Conn // Send / Receive; pipes now, sockets later
```

Layering (dependencies point down only):

```
cmd/kest ─▶ server ─▶ config, registry ─▶ metrics
                  ─▶ listener
                  ─▶ httpx (filter chain, router, RequestInfo) ◀─ filters/*
                  ─▶ cluster/* ─▶ httpx (RequestInfo, ClientIP, Scheme)
                               ─▶ rrhttp ─▶ worker ─▶ relay
                  ─▶ admin ─▶ metrics, cluster
```

`metrics` is a leaf that every component can use through `registry.BuildContext`.

`worker` and `relay` know nothing about HTTP; `rrhttp` maps HTTP requests and responses to payloads, and
`cluster/phpworkers` glues it to a pool.

### Adding an extension (cluster type or HTTP filter)

1. A package per extension: `internal/cluster/<name>/` or `internal/filters/<name>/`.
2. A config struct with snake_case `yaml` tags. `NewConfig` returns a pointer pre-filled with defaults.
   For field-level problems, `Validate() error` returns `*config.FieldError{Field, Msg}` values (joined with
   `errors.Join`); they come out located, as in `clusters[0].config.pool.workers: must be positive`.
3. Register in `init()`: `cluster.Types.Register(registry.Factory[cluster.Cluster]{Type, NewConfig, Build})`
   (or `httpx.Filters`). `Build` must not start goroutines, bind or connect; that belongs in `Start`. It may read
   files the config names (the `static` filter's `dir`) and report a problem as a `*config.FieldError`, which
   `registry.Build` locates like a `Validate` error. Core components located by YAML path use
   `config.Config.ErrorAt` (TLS certificates in `server.New`).
4. Blank-import the package from the binary, in `cmd/kest/extensions.go` (create it with the first
   extension). Otherwise `init()` never runs and the type is "unknown".
5. Tests: register test-only types from `_test.go` files (see `internal/server/server_test.go`), and add
   `TestMain` with `leaktest.VerifyTestMain` if the extension starts goroutines.
6. Document the type and its options in `docs/config.md`.

## 5. Worker lifecycle 🟢

Each pool slot is owned by one goroutine for the pool's lifetime. Slot states (`worker.State`):

```
Stopped ──▶ Starting ──▶ Ready ⇄ Working ──▶ Stopping ──▶ (released) ──▶ Starting …
                │           │        │                                     ▲
                └───────────┴────────┴──── failure ──▶ (released) ──▶ Backoff
```

| From | Event | To |
|---|---|---|
| Starting | handshake ok (within `StartTimeout`, which covers the app's boot) | Ready |
| Starting | spawn or handshake failed | Backoff (Start fails instead on the first start) |
| Ready | job handed over | Working |
| Ready | `idle_ttl`, `ttl`, memory over the limit, pool stop | Stopping |
| Ready | the process exited on its own | Backoff or Starting |
| Working | job done (or soft error), no limit reached | Ready |
| Working | job done, `max_jobs` / `ttl` / memory limit reached, or the worker asked to be replaced | Stopping |
| Working | crash, protocol error, `exec_ttl` exceeded | killed, then Backoff or Starting |
| Stopping | process exited after the stop frame, or SIGKILL after `destroy_timeout` | Starting (or done if the pool stops) |
| Backoff | 100 ms … 30 s elapsed (exponential, jitter) | Starting |
| Ready | `Pool.Reset` hands over a booted process | Ready with the new process; the old one stops in the background |

- A worker is never recycled mid-request, except on `exec_ttl` or a crash: that request fails.
- A caller that gives up (client gone, route timeout) doesn't kill the worker: the job finishes within
  `exec_ttl` and the result is dropped.
- A worker that asks to be replaced instead of answering (`$worker->stop()`) gets its job retried once on another.
- A streamed answer keeps the slot in `Working` until its last frame; the slot reads the frames, answers pings, and
  on `Stream.Close` (client gone) sends STOP and drains the rest within max(`exec_ttl`, `destroy_timeout`), or kills.
- `max_jobs` gets +0…14 % jitter (as in RoadRunner). `idle_ttl` counts from when the worker became ready, so an
  unused worker is replaced too. Only memory is polled (`watch_tick`); the other limits are timers and counters.
- Backoff applies after a worker fails without completing a job within 10 s of its start; the pool never
  panics, `Health()` reports it.
- Processes: explicit environment (`Config.Env` + `RR_RELAY=pipes`; `php_workers` builds it from kest's
  environment unless `inherit_env: false`, then `env`, then `RR_MODE=http`), own process group (SIGKILL hits the
  group), `Pdeathsig` on Linux, stderr logged line by line with `worker_pid`.

## 6. Startup, shutdown, reload 🟢 (config reload ⚪)

**Startup** (`config.Load` → `server.New` → `Server.Run`)

1. Parse flags → read file → parse YAML → expand env in scalars → strict decode → apply defaults → validate
   (including cross-references).
2. `server.New` builds the component graph through the registry, decoding extension configs and reading the
   files the config names (TLS certificates, the `static` filter's `dir`). No side effects, so `kest validate`
   runs this step too and reports unreadable files with their YAML path.
3. Start clusters (M2+: `php_workers` starts its pool and waits for every handshake, bounded by a startup
   timeout).
4. Start the admin server, then the listeners (bind ports; a TLS listener also starts its certificate checks,
   stopped in `Stop` after the drain). If anything fails to start, the components already started are stopped and
   `Run` returns the error.
5. Mark ready (`/ready` returns 200); `Server.Ready()` is closed.

**Shutdown** (SIGINT / SIGTERM, or a fatal error from a running component)

1. `/ready` → `503 draining`; on a signal, the listeners keep serving for `server.drain_delay` (default 0s).
2. Listeners stop accepting and drain in-flight requests in parallel, within `drain_timeout`; then the
   remaining connections are closed (logged as a warning, not an error).
3. Clusters stop, within the same budget again (M1: stop frame, then SIGKILL after `destroy_timeout`).
4. The admin server stops last (1s budget).
5. The first signal cancels the context and restores default signal handling, so a second signal kills
   the process immediately.

**Reload**

- M3 🟢: `php_workers.reload` (development) owns a watcher goroutine: `Start` scans its baseline after the pool has
  started, `Stop` stops it before the pool; a change that holds still for one scan triggers the reset below.
- M3 🟢: worker reset (rolling restart) via admin `POST /clusters/{name}/reset` (`cluster.Resetter`) or SIGHUP
  (`Server.ResetOn`; resets run until shutdown begins); config unchanged. `worker.Pool.Reset` boots each
  replacement while the old worker serves, hands it to the slot between jobs (`slot.swap`), and stops the old one in
  the background: capacity stays, at most `workers + 1` processes. A replacement that doesn't boot stops the reset;
  a slot whose process started after the reset began (a newer generation) drops the offered one.
- M4 🟢: TLS certificates: each TLS listener checks its files every 10s (size and mtime) and swaps a pair that
  changed once it holds still for a check; a pair that fails to load keeps the old one.
- M8: full config reload: diff the old and new graphs and restart only the components that changed.

## 7. Package layout

```
cmd/kest/                    🟢 CLI: serve, validate [-dump], version
internal/buildinfo/          🟢 version information (ldflags or embedded VCS data)
internal/config/             🟢 load, env expansion, strict decode, defaults, validation, dump
internal/registry/           🟢 extension categories and factories
internal/server/             🟢 build graph from config; start/stop order; drain
internal/listener/           🟢 HTTP listeners (bind in Start, drain in Stop; limits from the http block; TLS certificates, reloaded on change)
internal/httpx/              🟢 filter chain, router, route actions
internal/admin/              🟢 admin HTTP API
internal/logging/            🟢 slog setup
internal/cluster/            🟢 Cluster interface and registry category
internal/leaktest/           🟢 goroutine-leak check for test binaries (goleak)
internal/filters/            🟢 built-in HTTP filters, one package each: requestid/, accesslog/, recovery/, clientip/, headers/, cors/, compression/, static/
internal/cluster/phpworkers/ 🟢 php_workers: config, worker environment, pool lifecycle, HTTP handler
internal/rrhttp/             🟢 HTTP ↔ RoadRunner HTTP payload (request context JSON/protobuf, form trees, responses)
internal/worker/             🟢 process, pool, supervisor (language-agnostic); workertest/ has fake workers
internal/relay/              🟢 goridge-compatible frame codec and Conn (pipes; sockets later, K-029)
internal/metrics/            🟢 metrics core and text exposition (ADR-0007), Go and process metrics; metricstest/ for tests
internal/wasm/               ⚪ (M6) wazero runtime, module cache, instance pools
examples/                    🟢 runnable configs (PHP apps from M2)
docs/                        🟢 user documentation: getting started, config reference, metrics, operations, benchmarks
test/e2e/                    🟢 end-to-end tests: build the binary with -race, run it against configs
test/docker/                 🟢 the image of the PHP version matrix (make test-matrix)
test/docs/                   🟢 docs drift checks against the code and the Makefile (make docs-check)
test/bench/                  🟢 benchmark against RoadRunner: configs, loadgen, run.sh (make bench)
```

## 8. Observability and admin 🟡

- **Logs:** `log/slog`, text or JSON, level set in config. Worker stderr goes to logs line by line, tagged
  with `cluster` and `worker_pid` (🟢).
- **Metrics (M3):** requests (count, latency, status) per listener, virtual host and cluster (routes have no
  names); pool queue length
  and wait time; workers by state; restarts by reason; worker memory. A hand-written core in
  `internal/metrics` with lock-free updates and series resolved when the graph is built; `GET /metrics` serves
  the Prometheus text format 0.0.4 (ADR-0007). `server.New` creates the registry (with Go and process metrics)
  and hands it to components in `BuildContext`. `httpx` wraps each virtual host and counts a request under the cluster
  its route reached, if any (`routed` marks it in `RequestInfo`; labels: listener, virtual host, cluster, status
  code from a per-target array) and counts in-flight requests per listener; `php_workers`
  observes its pool (`worker.Observer`) and reads states, queue and memory at scrape time. M4 adds
  `kest_tls_*` (certificate expiry, failed handshakes) per listener.
- **Tracing:** OpenTelemetry later; the `request_id` filter exists (M3).
- **Admin API**, modeled on Envoy's admin interface:

| Endpoint | Purpose | Milestone |
|---|---|---|
| `GET /ready` | 200 when serving; 503 while starting, draining, or a cluster can't serve (`cluster.Prober`) | 🟢 M0, M3 |
| `GET /server_info` | version, uptime, config path | 🟢 M0 |
| `GET /config_dump` | effective config, defaults applied; extension configs that implement `config.Redactor` (php_workers: `env` values) redacted | 🟢 M3 |
| `GET /clusters` | clusters with `cluster.Reporter`'s status: php_workers pools and workers (pid, state, jobs, memory, age) | 🟢 M3 |
| `POST /clusters/{name}/reset` | rolling restart of a pool; returns when done | 🟢 M3 |
| `GET /metrics` | Prometheus text format (`docs/metrics.md`) | 🟢 M3 |

## 9. WASM sandbox (M6) ⚪

Runtime: wazero (ADR-0006, pure Go). Planned uses:

1. **`wasm` cluster:** a module handles a request, like a serverless function.
2. **`wasm` HTTP filter:** a module inspects or modifies requests and responses in the chain.
3. **Functions called from PHP:** a worker calls a sandboxed function through kest RPC (needs an RPC channel, see backlog).

Shared mechanics: compile once with a compilation cache, pool instances, set per-call memory limits and
deadlines (context cancellation), and use host functions for logging and config. The guest ABI is still
open: Proxy-Wasm, http-wasm, WASI stdin/stdout (CGI-style) or a custom one. It will be decided by an ADR
at the start of M6.

## 10. Open design questions

| ID | Question | Current leaning |
|---|---|---|
| Q-A | HTTP filter model: `func(http.Handler) http.Handler`, or Envoy-like decode/encode phases with per-filter state? | Middleware first; revisit when WASM filters arrive |
| Q-B | Request body: buffer it whole (RR-style, bounded by `max_request_body` and per cluster by `max_buffered_bodies`) or stream it to the worker? | Buffered (M2). The RR protocol sends the body in the request frame, so streaming it would need a protocol extension |
| Q-C | Streaming responses from PHP (SSE, large downloads) | ✅ M4 (K-088): `worker.Pool.ExecStream`; `php_workers` writes chunks as they come, 1xx as informational responses, STOP when the client goes away |
| Q-D | Cluster interface: `http.Handler`, or a kest request/response type shared by php, wasm and proxy? | ✅ M4: `http.Handler` stays |
| Q-E | Metrics: `prometheus/client_golang`, OpenTelemetry metrics, or a minimal custom exposition? | ✅ ADR-0007 (2026-10-25): a hand-written core with the Prometheus text format |
| Q-F | Per-route priority and queueing inside one pool, on top of pool isolation | After M3 |
| Q-G | PHP → kest RPC (RoadRunner's `rpc` plugin equivalent): needed for which PHP libraries? | Not for HTTP: `roadrunner/http` and `roadrunner/worker` don't use it (K-040); libraries for RR's KV, jobs, metrics, lock plugins do |

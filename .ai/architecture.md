# Architecture

> A living document. It describes the **target** design and marks what is implemented.
> Legend: 🟢 implemented · 🟡 in progress · ⚪ planned.
> **Last reviewed: 2026-10-27 (M7 in progress: RPC and plugins).** Implemented: config loading and validation, extension
> registry, server lifecycle, HTTP listeners on TCP and unix sockets with configurable limits, TLS (reloaded
> certificates) and HTTP/2/h2c,
> path normalization, router with `direct_response` and `redirect`, route names and matches on regex, method,
> headers and query; the goridge relay and the worker pool with rolling resets, streamed answers, lanes (worker
> limits, priorities, queue timeouts), overload control and a dynamic size (`internal/relay`, `internal/worker`);
> the RoadRunner HTTP payload
> (`internal/rrhttp`); the cluster type `php_workers` (body budget, `reload`); the filters `request_id`,
> `access_log`, `recovery`, `client_ip`, `headers`, `cors`, `compression`, `static`, `sendfile`, on listeners, virtual hosts and
> routes;
> metrics (`internal/metrics`); admin `/ready`, `/server_info`, `/config_dump`, `/clusters`,
> `POST /clusters/{name}/reset`, `/metrics`; the RPC server (`internal/rpc`) and the plugin category
> (`internal/plugin`), M7.

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
                │                               └─ wasm (M9)  ─▶ wazero instances       │       │
                │                                                                       │       │ RR_RPC
                │  RPC server (goridge RPC) ◀───────────────────────────────────────────┼───────┘
                │      └─ <name>.<Method> ─▶ plugins: kv (M7), external binaries (M7)   │
                │  Admin server (127.0.0.1:9901): /ready /server_info /config_dump        │
                │                                 /clusters /metrics                     │
                └───────────────────────────────────────────────────────────────────────┘
```

kest is a single Go binary. At startup it reads one YAML file and builds a graph of components:

- **Listeners** accept connections on an address (`host:port` or a unix socket) and hand them to a protocol handler
  (HTTP first).
- The **HTTP connection manager** runs a chain of **HTTP filters** (middleware), ending in the **router**.
- The **router** selects a **virtual host** by `Host` and runs its filters, then the first matching **route**,
  and runs its filters and its action: forward to a **cluster**, return a **direct response** or a **redirect**. Paths are normalized before
  the filters (`normalize_path`, `merge_slashes`).
- **Clusters** are named upstreams of different types (`php_workers`; `wasm` later).
- **Listeners** may terminate TLS (certificates by SNI, reloaded when their files change) and speak HTTP/2.
- A `php_workers` cluster owns a **pool** of processes, talks to each one through a **relay**
  using a framed protocol (ADR-0003), and is watched by a **supervisor**.
- The **admin server** exposes readiness, effective config, cluster and worker state, and metrics.
- **Plugins** are named components that PHP calls through the **RPC server** (M7, ADR-0010): goridge RPC on a unix
  socket, as RoadRunner's PHP libraries speak it. A call `kv.Set` goes to the plugin named `kv`.

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
6. The **pool** takes a free worker, waiting in its route's lane (a bounded queue) at most the lane's
   `queue_timeout` (the pool's `allocate_timeout` by default, `pool.overload.queue_timeout` in an overloaded lane),
   and sends the payload over the relay.
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
            - name: reports      # optional, unique: labels metrics and access log lines
              match: { prefix: /reports/ }
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
// Every request carries an *httpx.RequestInfo (RequestID, VirtualHost, Route, Cluster), set by the listener entry,
// the router and request_id; filters read it after the inner handler returns (access_log), whatever their
// order. httpx.ResponseWriter records the status and body size; httpx.BeforeHeader edits a response header just
// before it goes out (headers, cors, compression). RequestInfo.ClientIP/Scheme come from client_ip
// (httpx.ClientIP, httpx.Scheme). A unix socket listener clears r.RemoteAddr (its peer has no address: Go says "@"
// on Linux, "" on macOS); client_ip tells such peers by http.LocalAddrContextKey's network ("unix"). Built in: request_id, access_log, recovery, client_ip, headers, cors, compression,
// static, sendfile.

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
// Optional: Laner { Lane(httpx.Lane) (http.Handler, error) } (M5): a route with max_workers, priority or
// queue_timeout gets a handler of its own, in a lane of the cluster's pool; the router asks for it through
// httpx.ClusterLookup(cluster, *Lane) while the graph is built, and locates *config.FieldError at the route action.

// internal/plugin (M7, ADR-0010): what PHP calls through RPC, by the plugin's name. The interface moves to the
// public pkg/plugin with the SDK (K-168).
type Call struct {
	Method string // "Set" for kv.Set
	Codec  Codec  // goridge's codec bits: CodecProto, CodecJSON, CodecRaw, CodecMsgpack, CodecGob
	Body   []byte
}
type Plugin interface {
	Call(ctx context.Context, c Call) ([]byte, error) // the answer in c.Codec; ErrUnknownMethod for net/rpc's message
}
var Types = registry.NewCategory[Plugin]("plugin")
// Optional: Start/Stop, Prober { Serving() bool } (/ready).
// Built in: kv (internal/plugin/kv): storages behind one mutex each, a map, an LRU list and an expiry heap; expired
// items are invisible at once and swept every second; over max_size, expired then least recently used go.

// internal/rpc: the RPC server. Routes "<name>.<Method>" at the last dot, never decodes bodies; one call at a time
// per connection; answers and errors byte for byte as goridge's net/rpc codec (testdata/goridge_frames.txt).
func New(cfg Config, plugins map[string]plugin.Plugin, logger *slog.Logger, reg *metrics.Registry) (*Server, error)
func (s *Server) Address() string // RR_RPC: "unix:///path" (a private directory without rpc.address) or "tcp://host:port"
// BuildContext.RPCAddress carries it to php_workers, which adds RR_RPC to the workers' environment.
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
// Lanes (M5): queues of their own, added before Start; Exec and ExecStream above use the default lane.
func (p *Pool) Lane(cfg LaneConfig) (*Lane, error) // Name, MaxWorkers (0 = no limit), Priority, QueueTimeout
func (l *Lane) Exec / ExecStream                   // as the pool's, in the lane
func (p *Pool) Lanes() []LaneInfo                  // the default lane first: config, queued, busy
// A free worker takes the oldest job of the highest-priority lane under its MaxWorkers; MaxQueueSize bounds each
// lane's queue.
// A streamed answer (STREAM bit): the first frame, then Stream.Next(ctx) (Payload, ok, error) until ok is false;
// Stream.Close sends STOP and drops the rest. The slot goroutine answers PING with PONG; ExecTTL bounds each frame.
func (p *Pool) Workers() []Info                   // per slot: state, pid, jobs, started, memory
func (p *Pool) Health() Health                    // healthy / degraded / unhealthy
func (p *Pool) Waiting() int                      // callers waiting for a free worker
func (p *Pool) Reset(ctx context.Context) error   // rolling replacement, one slot at a time; ErrResetRunning
// Config.Observer (optional) hears Waited(lane, d), Rejected(lane, reason) and Replaced(reason); reasons are
// metric labels.
// Errors: ErrQueueFull, ErrNoWorker, ErrOverloaded (overload control), ErrStopped, ErrStopRequested,
// ErrStreamTooLarge (Exec only),
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
func NewConn(r io.Reader, w io.Writer, maxPayload int) *Conn // Send / Receive; pipes (socket relays: icebox, K-029)
```

Layering (dependencies point down only):

```
cmd/kest ─▶ server ─▶ config, registry ─▶ metrics
                  ─▶ listener
                  ─▶ httpx (filter chain, router, RequestInfo) ◀─ filters/*
                  ─▶ cluster/* ─▶ httpx (RequestInfo, ClientIP, Scheme, Lane)
                               ─▶ rrhttp ─▶ worker ─▶ relay
                  ─▶ admin ─▶ metrics, cluster
                  ─▶ rpc ─▶ plugin, relay, listener (unix sockets)
                  ─▶ plugin ─▶ registry
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

Extra slots (beyond Workers, M5): Parked ──▶ Starting ──▶ Ready ⇄ Working ──▶ Stopping ──▶ Parked
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
| Backoff | 100 ms … 30 s elapsed (exponential, jitter) | Starting (Parked for an extra slot) |
| Parked | an extra slot (beyond `Workers`) is woken: a job waits that its lane would let through (M5) | Starting |
| Ready | an extra worker idle for `ScaleDownAfter`, or its process ends | Stopping, then Parked |
| Ready | `Pool.Reset` hands over a booted process | Ready with the new process; the old one stops in the background |

- Dispatch (`lane.go`): a ready slot parks: under the pool's mutex it takes the next job a lane may hand out, or joins
  the idle list (longest idle first); a new job goes straight to an idle slot. Jobs reach a slot on its own channel
  (capacity 1), so a parked slot that leaves for another event (a limit, a reset, stop) takes itself off the idle
  list and gives back a job handed to it meanwhile, to the head of its lane (failed instead if its queue timeout has
  passed or the pool stops, dropped if its caller is gone). A lane's busy count drops when the slot is done with the
  job, the last stream frame included.
- Growing (`grow.go`, M5): slots `Workers`…`MaxWorkers-1` are extra. Their goroutine (`runExtra`) parks them without a
  process until `growLocked` wakes one: when a queued job that its lane would let through finds no free worker, at
  most `max(1, Workers/4)` booting at once, and no more than such jobs wait. A boot counts until the new worker first
  parks (and takes a job). Idle extra workers stop after `ScaleDownAfter`; base slots get jobs first (`idle` before
  `idleExtra`), so the extra ones go idle. `Health` counts the base slots only.
- Overload control (CoDel, `OverloadQueueTimeout`/`OverloadInterval`, 500ms/5s by default): a lane is overloaded
  once jobs have waited in it without a break for the interval (`backlogSince`, reset only when a worker takes the
  last waiting job). Its new jobs then wait at most the overload timeout (`ErrOverloaded`), and `nextLocked` sheds
  the heads that waited longer. Lanes with `MaxWorkers` or `QueueTimeout` are left out.
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
3. Start plugins, then the RPC server (M7: binds `rpc.address` or a socket in a private temporary directory), then
   clusters (M2+: `php_workers` starts its pool and waits for every handshake, bounded by a startup timeout), so
   workers can call plugins while they boot.
4. Start the admin server, then the listeners (bind ports; a TLS listener also starts its certificate checks,
   stopped in `Stop` after the drain). A unix socket listener removes a stale socket file first (one whose connect is
   refused; a live socket or a file that isn't a socket is an error), binds under a temporary name in the same
   directory, sets `socket_mode`, and links the socket into place, so nobody connects before the mode applies and a
   file that appeared meanwhile isn't replaced. In `Stop` it removes the file, if it is still its socket
   (`os.SameFile`). If anything fails to start, the components already started are stopped and
   `Run` returns the error.
5. Mark ready (`/ready` returns 200); `Server.Ready()` is closed.

**Shutdown** (SIGINT / SIGTERM, or a fatal error from a running component)

1. `/ready` → `503 draining`; on a signal, the listeners keep serving for `server.drain_delay` (default 0s).
2. Listeners stop accepting and drain in-flight requests in parallel, within `drain_timeout`; then the
   remaining connections are closed (logged as a warning, not an error).
3. Clusters stop, within the same budget again (M1: stop frame, then SIGKILL after `destroy_timeout`).
4. The RPC server, then plugins, each within the drain timeout (M7): workers call plugins until they exit. The RPC
   server closes idle connections at once, lets calls in progress answer, and cancels them at its deadline.
5. The admin server stops last (1s budget).
6. The first signal cancels the context and restores default signal handling, so a second signal kills
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
internal/listener/           🟢 HTTP listeners on TCP or unix sockets (bind in Start, drain in Stop; limits from the http block; TLS certificates, reloaded on change)
internal/httpx/              🟢 filter chain, router, route actions
internal/admin/              🟢 admin HTTP API
internal/logging/            🟢 slog setup
internal/cluster/            🟢 Cluster interface and registry category
internal/leaktest/           🟢 goroutine-leak check for test binaries (goleak)
internal/filters/            🟢 built-in HTTP filters, one package each: requestid/, accesslog/, recovery/, clientip/, headers/, cors/, compression/, static/, sendfile/
internal/cluster/phpworkers/ 🟢 php_workers: config, worker environment, pool lifecycle, HTTP handler
internal/plugin/             🟡 plugin interface and registry category (M7, ADR-0010)
internal/plugin/kv/          🟢 kv: in-memory storages for roadrunner/kv (TTL, LRU within max_size, hand-written kv.v1 protobuf)
internal/rpc/                🟡 the RPC server: goridge RPC from workers, routed to plugins (M7)
internal/rrhttp/             🟢 HTTP ↔ RoadRunner HTTP payload (request context JSON/protobuf, form trees, responses)
internal/worker/             🟢 process, pool, supervisor (language-agnostic); workertest/ has fake workers
internal/relay/              🟢 goridge-compatible frame codec and Conn (pipes; socket relays in the icebox, K-029)
internal/metrics/            🟢 metrics core and text exposition (ADR-0007), Go and process metrics; metricstest/ for tests
internal/wasm/               ⚪ (M9) wazero runtime, module cache, instance pools
examples/                    🟢 runnable configs (PHP apps from M2)
docs/                        🟢 user documentation: getting started, config reference, metrics, operations, benchmarks
test/e2e/                    🟢 end-to-end tests: build the binary with -race, run it against configs
test/docker/                 🟢 the image of the PHP version matrix (make test-matrix)
test/docs/                   🟢 docs drift checks against the code and the Makefile (make docs-check)
test/bench/                  🟢 benchmark against RoadRunner: configs, loadgen, codec.php, run.sh (make bench)
```

## 8. Observability and admin 🟡

- **Logs:** `log/slog`, text or JSON, level set in config. Worker stderr goes to logs line by line, tagged
  with `cluster` and `worker_pid` (🟢).
- **Metrics (M3):** requests (count, latency, status) per listener, virtual host, route name (M5) and cluster;
  pool queue length and wait time; workers by state; restarts by reason; worker memory. A hand-written core in
  `internal/metrics` with lock-free updates and series resolved when the graph is built; `GET /metrics` serves
  the Prometheus text format 0.0.4 (ADR-0007). `server.New` creates the registry (with Go and process metrics)
  and hands it to components in `BuildContext`. `httpx` wraps each virtual host and counts a request under the named
  route it chose and the cluster that route reached, if any (`chosen` and `routed` mark them in `RequestInfo`;
  labels: listener, virtual host, route, cluster, status code from a per-target array) and counts in-flight requests
  per listener; with listener filters, the listener's entry counts the requests they answer before the router
  (`RequestInfo.counted` unset) without a virtual host; `php_workers`
  observes its pool (`worker.Observer`, by lane: the `route` label of `kest_pool_wait_seconds`,
  `kest_pool_rejected_total`, `kest_pool_queue_length`, `kest_pool_lane_workers`) and reads states, queues and memory
  at scrape time. M4 adds
  `kest_tls_*` (certificate expiry, failed handshakes) per listener.
- **RPC (M7):** `kest_rpc_connections`, `kest_rpc_refused_connections_total`, `kest_rpc_calls_total{plugin, result}`,
  `kest_rpc_call_duration_seconds{plugin}`; the plugin label is a configured name, never what a client sent.
- **Tracing:** OpenTelemetry later; the `request_id` filter exists (M3).
- **Admin API**, modeled on Envoy's admin interface:

| Endpoint | Purpose | Milestone |
|---|---|---|
| `GET /ready` | 200 when serving; 503 while starting, draining, or a cluster can't serve (`cluster.Prober`) | 🟢 M0, M3 |
| `GET /server_info` | version, uptime, config path | 🟢 M0 |
| `GET /config_dump` | effective config, defaults applied; extension configs that implement `config.Redactor` (php_workers: `env` values) redacted | 🟢 M3 |
| `GET /clusters` | clusters with `cluster.Reporter`'s status: php_workers pools, their lanes (M5) and workers (pid, state, jobs, memory, age) | 🟢 M3 |
| `POST /clusters/{name}/reset` | rolling restart of a pool; returns when done | 🟢 M3 |
| `GET /metrics` | Prometheus text format (`docs/metrics.md`) | 🟢 M3 |

## 9. WASM sandbox (M9) ⚪

Runtime: wazero (ADR-0006, pure Go). Planned uses:

1. **`wasm` cluster:** a module handles a request, like a serverless function.
2. **`wasm` HTTP filter:** a module inspects or modifies requests and responses in the chain.
3. **Functions called from PHP:** a worker calls a sandboxed function through kest RPC (needs an RPC channel, see backlog).

Shared mechanics: compile once with a compilation cache, pool instances, set per-call memory limits and
deadlines (context cancellation), and use host functions for logging and config. The guest ABI is still
open: Proxy-Wasm, http-wasm, WASI stdin/stdout (CGI-style) or a custom one. It will be decided by an ADR
at the start of M9.

## 10. Open design questions

| ID | Question | Current leaning |
|---|---|---|
| Q-A | HTTP filter model: `func(http.Handler) http.Handler`, or Envoy-like decode/encode phases with per-filter state? | Middleware first; revisit when WASM filters arrive |
| Q-B | Request body: buffer it whole (RR-style, bounded by `max_request_body` and per cluster by `max_buffered_bodies`) or stream it to the worker? | Buffered (M2). The RR protocol sends the body in the request frame, so streaming it would need a protocol extension |
| Q-C | Streaming responses from PHP (SSE, large downloads) | ✅ M4 (K-088): `worker.Pool.ExecStream`; `php_workers` writes chunks as they come, 1xx as informational responses, STOP when the client goes away |
| Q-D | Cluster interface: `http.Handler`, or a kest request/response type shared by php, wasm and proxy? | ✅ M4: `http.Handler` stays |
| Q-E | Metrics: `prometheus/client_golang`, OpenTelemetry metrics, or a minimal custom exposition? | ✅ ADR-0007 (2026-10-25): a hand-written core with the Prometheus text format |
| Q-F | Per-route priority and queueing inside one pool, on top of pool isolation | ✅ M5: lanes with a worker limit, strict priority and a queue timeout per route (`plans/M5-pools.md` D2) |
| Q-G | PHP → kest RPC (RoadRunner's `rpc` plugin equivalent): needed for which PHP libraries? | ✅ M7 (2026-10-26): the RPC channel, compatible with `roadrunner/goridge`, and plugins by name, `kv` first (`roadrunner/kv`); RoadRunner's own services and metrics from PHP not now (`plans/M7-rpc.md`) |

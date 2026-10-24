# Architecture

> A living document. It describes the **target** design and marks what is implemented.
> Legend: 🟢 implemented · 🟡 in progress · ⚪ planned.
> **Last reviewed: 2026-10-24 (M0 done).** Implemented: config loading and validation, extension
> registry, server lifecycle, HTTP listeners, router with `direct_response`, admin `/ready` and
> `/server_info`. No cluster types or filters exist yet.

## 1. Big picture 🟡

```
                ┌──────────────────────────────── kest ─────────────────────────────────┐
                │                                                                       │
  client ─────▶ │  Listener ──▶ HTTP connection manager                                 │
                │               ├─ filter: request_id                                   │
                │               ├─ filter: access_log                                   │
                │               ├─ filter: …                                            │
                │               └─ router (implicit, last)                              │
                │                    │ vhost by Host, first matching route              │
                │                    ├─ direct_response                                 │
                │                    └─ route → Cluster                                 │
                │                               ├─ php_workers ─▶ Pool ─▶ Relay ────────┼──▶ php worker.php × N
                │                               ├─ wasm (M5)  ─▶ wazero instances       │
                │                               └─ http_proxy (M4) ─────────────────────┼──▶ remote endpoints
                │                                                                       │
                │  Admin server (127.0.0.1:9901): /ready /server_info /config_dump        │
                │                                 /clusters /metrics                     │
                └───────────────────────────────────────────────────────────────────────┘
```

kest is a single Go binary. At startup it reads one YAML file and builds a graph of components:

- **Listeners** accept connections on an address and hand them to a protocol handler (HTTP first).
- The **HTTP connection manager** runs a chain of **HTTP filters** (middleware), ending in the **router**.
- The **router** selects a **virtual host** by `Host` and the first matching **route**, then runs its
  action: forward to a **cluster** or return a **direct response**.
- **Clusters** are named upstreams of different types (`php_workers`, `wasm`, `http_proxy`).
- A `php_workers` cluster owns a **pool** of processes, talks to each one through a **relay**
  using a framed protocol (ADR-0003), and is watched by a **supervisor**.
- The **admin server** exposes readiness, effective config, cluster and worker state, and metrics.

## 2. Request flow: HTTP → PHP ⚪ (steps 1–4 🟢)

1. The listener accepts a connection; the `net/http` server parses the request.
2. HTTP filters run in config order (e.g. `request_id` → `access_log` → `cors`).
3. The router picks a virtual host by `Host`, then the first route whose `match` fits.
4. Action `route: {cluster: php_app, timeout: 30s}` → the request context gets the route timeout.
5. The `php_workers` cluster encodes the request into a **payload**: `context` = request head
   (method, URI, headers, cookies, remote address, …) encoded with a codec; `body` = raw bytes, bounded
   by `max_request_body`.
6. The **pool** takes a free worker, waiting at most `allocate_timeout` in a bounded queue, and sends
   the payload over the relay.
7. The PHP worker (`$worker->waitRequest()` … `$worker->respond()`) answers; kest decodes status,
   headers and body and writes the response.
8. The worker goes back to the pool. The supervisor checks its limits (max_jobs, memory, TTL) and
   recycles it if needed.

## 3. Configuration model 🟢

Decided in ADR-0002 (Proposed). Full example; fields marked in `docs/config.md` as existing are
implemented, the rest (filters, `php_workers`) are planned:

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
      max_request_body: 16MB
      filters:               # run in order; the router is implicit and always last
        - type: request_id
        - type: access_log
          config:
            format: json
      virtual_hosts:
        - name: app
          domains: ["example.com", "*.example.com"]
          routes:
            - match: { prefix: /healthz }
              direct_response: { status: 200, body: "ok" }
            - match: { prefix: /reports/ }
              route: { cluster: php_heavy, timeout: 120s }
            - match: { prefix: / }
              route: { cluster: php_app, timeout: 30s }

clusters:
  - name: php_app
    type: php_workers
    config:
      command: [php, public/worker.php]
      env: { APP_ENV: prod }
      relay: pipes
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
- The effective config (defaults applied, secrets redacted) is available through
  `kest validate --dump` and admin `/config_dump`.

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
	Build     func(bc BuildContext, cfg any) (T, error) // no side effects: no goroutines, no I/O
}
type BuildContext struct {
	Name   string // component name (clusters)
	Logger *slog.Logger
}
func Build[T any](c *Category[T], ext *config.Extension, bc BuildContext) (T, error)

// internal/httpx: filters are plain middleware for now (open question Q-A).
type Filter func(next http.Handler) http.Handler
var Filters = registry.NewCategory[Filter]("http filter")

// internal/cluster: the name lives in config/BuildContext, not on the interface.
type Cluster interface {
	http.Handler // must honor the request context; answers 504 on route timeout (Q-D)
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}
var Types = registry.NewCategory[Cluster]("cluster")
```

Planned ⚪:

```go
// Worker pool: language-agnostic, knows nothing about HTTP or PHP.
type Payload struct {
	Codec   Codec  // how Context is encoded: raw, json, proto
	Context []byte // metadata, e.g. the encoded HTTP request head
	Body    []byte
}

type Pool interface {
	Component
	Exec(ctx context.Context, p Payload) (Payload, error)
	Reset(ctx context.Context) error // rolling restart of all workers
	Workers() []WorkerInfo
}

// Relay: transport and framing to a single worker process.
type Relay interface {
	Send(f *Frame) error
	Receive(f *Frame) error
	io.Closer
}
```

Layering (dependencies point down only):

```
cmd/kest ─▶ server ─▶ config, registry
                  ─▶ listener ─▶ httpx (filters, router)
                  ─▶ cluster/* ─▶ worker ─▶ relay
                  ─▶ admin
```

`worker` and `relay` know nothing about HTTP; `cluster/phpworkers` maps HTTP to payloads.

## 5. Worker lifecycle ⚪

```
Starting ──▶ Ready ⇄ Working ──▶ Stopping ──▶ Stopped ──▶ replaced (unless the pool is stopping)
    │                   │                        ▲
    └───────────────────┴──▶ Errored ────────────┘
```

| From | Event | To |
|---|---|---|
| Starting | handshake ok | Ready |
| Starting | handshake failed / startup timeout | Errored |
| Ready | exec | Working |
| Ready | `idle_ttl` or `ttl` reached, reset, pool stop | Stopping |
| Working | job done, limits ok | Ready |
| Working | job done, `max_jobs` / `max_worker_memory` / `ttl` reached | Stopping |
| Working | crash, `exec_ttl` exceeded, protocol error | Errored |
| Stopping | process exited (stop frame; SIGKILL after `destroy_timeout`) | Stopped |
| Errored | process killed | Stopped |

Supervisor limits per pool: `max_jobs`, `ttl`, `idle_ttl`, `exec_ttl`, `max_worker_memory`.
A worker is never recycled mid-request, except on `exec_ttl` or a crash, in which case the request fails
with a 5xx and is logged. Respawn uses exponential backoff so a broken worker script can't
fork-bomb the host. The pool reports "unhealthy" if it can't keep its minimum of live workers.

## 6. Startup, shutdown, reload 🟢 (reload ⚪)

**Startup** (`config.Load` → `server.New` → `Server.Run`)

1. Parse flags → read file → parse YAML → expand env in scalars → strict decode → apply defaults → validate
   (including cross-references).
2. `server.New` builds the component graph through the registry, decoding extension configs. No side
   effects, so `kest validate` runs this step too.
3. Start clusters (M1+: spawn workers and wait for readiness, bounded by a startup timeout).
4. Start the admin server, then the listeners (bind ports). If anything fails to start, the components
   already started are stopped and `Run` returns the error.
5. Mark ready (`/ready` returns 200); `Server.Ready()` is closed.

**Shutdown** (SIGINT / SIGTERM, or a fatal error from a running component)

1. `/ready` → `503 draining`.
2. Listeners stop accepting and drain in-flight requests in parallel, within `drain_timeout`; then the
   remaining connections are closed (logged as a warning, not an error).
3. Clusters stop, within the same budget again (M1: stop frame, then SIGKILL after `destroy_timeout`).
4. The admin server stops last (1s budget).
5. The first signal cancels the context and restores default signal handling, so a second signal kills
   the process immediately.

**Reload**

- M3: worker reset (rolling restart) via admin `POST /clusters/{name}/reset` or SIGHUP; config unchanged.
- M6: full config reload: diff the old and new graphs and restart only the components that changed.

## 7. Package layout

```
cmd/kest/                    🟢 CLI: serve, validate [-dump], version
internal/buildinfo/          🟢 version information (ldflags or embedded VCS data)
internal/config/             🟢 load, env expansion, strict decode, defaults, validation, dump
internal/registry/           🟢 extension categories and factories
internal/server/             🟢 build graph from config; start/stop order; drain
internal/listener/           🟢 HTTP listeners (bind in Start, drain in Stop)
internal/httpx/              🟢 filter chain, router, route actions
internal/admin/              🟢 admin HTTP API
internal/logging/            🟢 slog setup
internal/cluster/            🟢 Cluster interface and registry category
internal/filters/<name>/     ⚪ built-in HTTP filters, one package each
internal/cluster/phpworkers/ ⚪ php_workers: HTTP ↔ RoadRunner payload mapping
internal/worker/             ⚪ process, pool, supervisor (language-agnostic)
internal/relay/              ⚪ goridge-compatible frame codec; pipes and socket transports
internal/metrics/            ⚪ (M3) metrics
internal/wasm/               ⚪ (M5) wazero runtime, module cache, instance pools
examples/                    🟢 runnable configs (PHP apps from M2)
docs/                        🟢 user documentation (config reference)
test/e2e/                    🟢 end-to-end tests: build the binary with -race, run it against configs
```

## 8. Observability and admin 🟡

- **Logs:** `log/slog`, text or JSON, level set in config. Worker stderr goes to logs tagged with
  `cluster` and `worker_pid`.
- **Metrics (M3):** requests (count, latency, status) per listener, route and cluster; pool queue length
  and wait time; workers by state; restarts by reason; worker memory. Library to be chosen (Q-E).
- **Tracing:** OpenTelemetry later; the `request_id` filter arrives in M3.
- **Admin API**, modeled on Envoy's admin interface:

| Endpoint | Purpose | Milestone |
|---|---|---|
| `GET /ready` | 200 when serving, 503 while starting or draining | 🟢 M0 |
| `GET /server_info` | version, uptime, config path | 🟢 M0 |
| `GET /config_dump` | effective config, defaults applied, secrets redacted | M3 |
| `GET /clusters` | clusters, pools, workers (pid, state, jobs, memory, age) | M3 |
| `POST /clusters/{name}/reset` | rolling restart of a pool | M3 |
| `GET /metrics` | Prometheus exposition | M3 |

## 9. WASM sandbox (M5) ⚪

Runtime: wazero (ADR-0006, pure Go). Planned uses:

1. **`wasm` cluster:** a module handles a request, like a serverless function.
2. **`wasm` HTTP filter:** a module inspects or modifies requests and responses in the chain.
3. **Functions called from PHP:** a worker calls a sandboxed function through kest RPC (needs an RPC channel, see backlog).

Shared mechanics: compile once with a compilation cache, pool instances, set per-call memory limits and
deadlines (context cancellation), and use host functions for logging and config. The guest ABI is still
open: Proxy-Wasm, http-wasm, WASI stdin/stdout (CGI-style) or a custom one. It will be decided by an ADR
at the start of M5.

## 10. Open design questions

| ID | Question | Current leaning |
|---|---|---|
| Q-A | HTTP filter model: `func(http.Handler) http.Handler`, or Envoy-like decode/encode phases with per-filter state? | Middleware first; revisit when WASM filters arrive |
| Q-B | Request body: buffer it whole (RR-style, bounded by `max_request_body`) or stream it to the worker? | Buffer in M2; streaming later |
| Q-C | Streaming responses from PHP (SSE, large downloads) | Research in K-040; implement after M2 |
| Q-D | Cluster interface: `http.Handler`, or a kest request/response type shared by php, wasm and proxy? | `http.Handler` until a second cluster type exists |
| Q-E | Metrics: `prometheus/client_golang`, OpenTelemetry metrics, or a minimal custom exposition? | Decide at the start of M3 |
| Q-F | Per-route priority and queueing inside one pool, on top of pool isolation | After M3 |
| Q-G | PHP → kest RPC (RoadRunner's `rpc` plugin equivalent): needed for which PHP libraries? | Research in K-040 |

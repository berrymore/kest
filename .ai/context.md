# Context

The *why* and *what* of kest. Change this file only together with the user.

## One-liner

kest is a Go application server that runs PHP applications in long-lived worker processes
(RoadRunner-style) behind a proxy such as nginx, configured as an explicit Envoy-style graph in one YAML
file. It takes off PHP's hands what PHP does badly, so that PHP keeps the business logic; a WASM sandbox for
fast, isolated functions comes later.

## Problem

- Classic PHP-FPM boots the whole framework on every request. Long-lived workers remove that cost,
  but they need a solid process manager in front of them.
- RoadRunner solves this well. kest explores a different angle on configuration and composition:
  an explicit graph of listeners, routes and clusters, several isolated pools in one process, and
  first-class sandboxed WASM next to PHP.
- Hot paths (image resizing, token checks, request transforms, routing decisions) often don't need a
  full PHP worker. A WASM sandbox can serve them in microseconds, with strong isolation.

## Goals

Near term (M0–M3):

1. Run **unmodified RoadRunner PHP HTTP workers** (PSR-7) behind kest.
2. **Envoy-like declarative config:** one YAML file, strict validation (`kest validate`), effective
   config dump with defaults.
3. **Several listeners, pools and routes in one process:** route by host and path to different pools.
4. **Production-grade supervision:** bounded queues, timeouts, memory/TTL/job limits, crash recovery,
   graceful shutdown, rolling restarts.
5. **Operability:** structured logs, metrics, admin API.

Longer term (M4+):

6. **A professional upstream for the proxy in front** (ADR-0008): HTTP/1.1 and HTTP/2 (h2c), the client's
   address from trusted proxies, streamed responses, the files of `public/` that the proxy can't see (M4);
   unix socket listeners (M6); gRPC (M10).
7. **Worker pools and routing to them**: lanes that keep a slow endpoint from taking the whole pool, priorities,
   fast rejection under overload, a pool that grows under load, `X-Sendfile`, richer route matches (M5).
   Filters that take work off PHP (authentication, limits) come later.
8. **Connections PHP can't hold**: WebSockets and SSE held by kest while PHP handles the events, when an
   application needs them (researched, `research/websockets.md`).
9. **PHP → kest RPC**, the channel RoadRunner's PHP libraries call (M7).
10. Dynamic configuration: hot reload without dropping connections (M8).
11. **WASM sandbox** on wazero: `wasm` clusters (handlers), `wasm` HTTP filters, possibly functions
    callable from PHP (M9). An option, so it comes near the end.
12. More plugins (queues/jobs, KV) once the core is solid.

## Non-goals (for now)

- **Being an edge proxy** (ADR-0008). kest stands behind nginx, an ingress or a load balancer, and takes on
  what concerns how that proxy talks to the application or what makes up for a weakness of PHP. No ACME, no
  brotli, no full file server, no reverse proxy or load balancing to other services.
- Full Envoy parity, service mesh, or xDS wire compatibility.
- Config compatibility with `.rr.yaml`. Worker-side (PHP) compatibility, yes; config compatibility, no.
- Embedding PHP in the Go process via CGO (the FrankenPHP approach). kest uses external worker processes (ADR-0003).
- Runtime-loaded plugins (Go `plugin` / `.so`). Extensions are compiled in (ADR-0004).
- Windows support (Linux and macOS only, at least until 1.0).

## Principles

1. **Explicit over magic.** The config file is the whole truth; the effective config (with defaults)
   can always be dumped.
2. **Fail fast at startup.** Validate everything, including cross-references, before binding ports or
   spawning processes.
3. **Everything bounded.** Every queue, buffer, timeout and process has a limit.
4. **Graceful by default.** Drain on shutdown, recycle workers without dropping requests.
5. **Observable by default.** If it can break in production, it has a metric or a log line.
6. **Small core, pluggable edges.** Listeners, filters and clusters are extensions behind a registry.
7. **Stdlib first, pure Go.** Few dependencies, no CGO by default (ADR-0005).

## Supported platforms

- **OS:** Linux and macOS.
- **PHP:** kest doesn't restrict the PHP version; the worker protocol is version-agnostic. The effective
  floor comes from the PHP worker libraries (currently `php >=8.2`, see `research/roadrunner.md`).
  We develop on PHP 8.5 and test against 8.3+ (matrix 8.3 / 8.4 / 8.5).
- **Go:** 1.27+ to build.

## Users

- PHP teams who want RoadRunner-style performance with an explicit, routable config.
- Platform engineers who want one binary per service with predictable operations.
- Later: teams who want to move hot paths into WASM without running a separate service.

## Glossary

| Term | Meaning in kest |
|---|---|
| **Listener** | Named network endpoint (address and protocol) that accepts connections |
| **HTTP filter** | Middleware step in the HTTP chain of a listener, a virtual host or a route (request_id, cors, …) |
| **Router** | Implicit last step of the HTTP chain; picks a virtual host and a route |
| **Virtual host** | Set of routes selected by the `Host` header (`domains`) |
| **Route** | Match rule (prefix, path, …) plus an action: `route` to a cluster, `direct_response` or `redirect` |
| **Cluster** | Named upstream that serves requests: `php_workers`, `wasm` (planned) |
| **Pool** | Set of worker processes owned by a cluster |
| **Lane** | A route's own queue in a pool, with a limit on the workers it takes, a priority and a queue timeout (M5) |
| **Worker** | One long-lived child process (e.g. `php worker.php`) handling one request at a time |
| **Supervisor** | Watches workers and recycles them on limits: TTL, idle TTL, exec TTL, memory, max jobs |
| **Relay** | Transport between kest and a worker: pipes (stdin/stdout); socket relays are in the icebox (K-029) |
| **Frame** | Unit of the wire protocol on a relay (goridge-compatible, ADR-0003) |
| **Payload** | Request/response unit sent to a worker: `context` (metadata) + `body` |
| **Codec** | Encoding of the payload context: raw, JSON, protobuf |
| **Extension** | Compiled-in component type registered by name in a category (filter, cluster, …) |
| **Sandbox** | Isolated WASM instance with memory and time limits (M9) |
| **Admin server** | Separate HTTP endpoint for readiness, config dump, cluster state, metrics |

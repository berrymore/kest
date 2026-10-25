# ADR-0008: Position kest as an application server behind a proxy, not an edge proxy

- **Status:** Accepted (2026-10-25)
- **Date:** 2026-10-25
- **Deciders:** user, agent

## Context

`context.md` listed "HTTP edge features: TLS, HTTP/2, static files, compression, reverse proxy" as a goal, and M4
built them. With an `http_proxy` cluster (load balancing, health checks, panic mode, retries: 1,200 lines and five
metrics) and a file server in two forms (`php_workers.static` and the `static_files` cluster), kest began to compete
with nginx, Envoy, Caddy and Traefik. That is not what it is for.

Facts:

- In the deployments kest is for, a proxy always stands in front of it: nginx, a Kubernetes ingress, a cloud load
  balancer. That proxy faces the internet: TLS for browsers, ACME, CDN-grade file serving, brotli, balancing over
  many hosts.
- What PHP and PHP-FPM lack is on the other side: the framework boots on every request, a process serves one
  connection at a time, slow clients and large files hold a process, long-lived connections are out of reach. kest's
  value is there: long-lived workers, several pools in one process, routing between them, and filters that do work
  before and after PHP so that PHP keeps the business logic.
- A Kubernetes ingress doesn't see the files of the application's container, so an application server still needs to
  serve `public/` (RoadRunner's `static` middleware and FrankenPHP's file server do).
- `php_workers.static` made a pool of worker processes also a file server. File responses counted as requests to the
  PHP cluster (`kest_http_request_duration_seconds{cluster="app"}`), mixing them into the latency of PHP.
- A filter on the listener can't take its place: listener filters run before the router, for every virtual host, so
  it would serve one application's files on another's virtual host, and past a route that denies the path.
- nginx's `grpc_pass` speaks HTTP/2 to its upstream without TLS (h2c with prior knowledge) ❓, and Envoy and cloud load
  balancers can. HTTP/2 is a protocol of the upstream side too, and gRPC, which RoadRunner offers to PHP, needs it.

## Decision

We will build kest as an application server that stands behind a proxy, not as an edge proxy. kest takes on a
protocol or a feature when it

1. concerns how a proxy talks to the application: HTTP/1.1 and HTTP/2 (h2c), gRPC, unix sockets, the client's
   address from trusted proxies; or
2. makes up for a weakness of PHP: long-lived connections, slow clients and large files holding a worker, work that
   needn't take a worker (authentication, CORS, limits, routing between pools).

kest doesn't take on what faces the internet (ACME, brotli and zstd, precompressed files, a full file server, client
certificates) or what proxies to other services (load balancing, health checks and retries towards their endpoints).

Applied to M4, before its merge:

- The `http_proxy` cluster is removed (K-085). The cluster interface stays `http.Handler`.
- Static files become one filter, `static`: a `GET` or `HEAD` for a file that exists is answered by kest, anything
  else goes on to the route's action (nginx's `try_files $uri @app`). `php_workers.static` and the `static_files`
  cluster are removed (K-082).
- Filters can be set on a virtual host and on a route, and run after the router has chosen them (K-096), so `static`
  serves only the routes it is set on. A response that such a filter makes itself counts with an empty `cluster`
  label, like a direct response.
- HTTP/2, h2c included, is core. TLS from files and gzip compression stay as they are and don't grow.
- Path normalization, `client_ip`, `headers`, `cors`, `redirect`, streamed responses and early hints stay.

The roadmap follows:

- M5 "Routing, filters and pools": the core, before WASM.
- M6 WASM sandbox (was M5).
- M7 "Protocols": gRPC on the HTTP/2 listener, routed like any request, with kest's own errors as gRPC statuses;
  WebSockets and SSE held by kest while PHP handles the events; unix socket listeners.
- M8 Dynamic configuration (was M6).
- A FastCGI listener (nginx's `fastcgi_pass` to kest in place of PHP-FPM) waits in the icebox as a bonus.

## Alternatives considered

- **Keep M4 as built** — the features work and are tested; but each duplicates the proxy in front, needs maintenance
  and security review forever, and invites more of the same (passive health checks, brotli, ACME).
- **Static files only as a cluster type** (`static_files`) — fits the cluster model, but a route forwards to one
  cluster, so "the file if it exists, else PHP" can't be said; that is the common case.
- **A static filter on the listener** — no new concept, but it serves files on every virtual host and past routes
  that deny the path.
- **No static files at all** — the proxy in front serves them where it can see them; it can't behind an ingress.

## Consequences

- Positive: less to maintain and to secure; a test for the features to come; the PHP cluster's metrics describe
  PHP; filters on virtual hosts and routes are the base for M5 and for WASM filters.
- Negative / costs: whoever wants kest without a proxy in front gets no reverse proxy from it; the config schema
  changes once more before the first release (nothing was released, so nobody migrates).
- Follow-ups: K-085 removed, K-082 redone, K-096; dropped: K-091, K-092, K-093, K-094, K-906; `context.md` (goals,
  non-goals, glossary), `roadmap.md` and `backlog.md` (M5, M7, renumbered M6 and M8).

# Plan: M4 — HTTP edge features

- **Status:** Done (2026-10-25), merged in PR #3. The user delegated M4 ("start M4, write the plan, try to finish
  the milestone yourself") and asked not to open the PR before they had looked at the result locally. The decisions
  below were taken by the agent; the ones marked **schema** change the config file and were listed for the user's
  review. After that look, the user narrowed the milestone (ADR-0008, step 13): D9 and D11 are superseded by D12.
- **Milestone:** M4
- **Backlog:** K-080…K-090, K-096 (K-084 split into K-084, K-089, K-090 on 2026-10-25; K-085 removed)

## Goal

kest can be the edge of a PHP application: it terminates TLS from certificate files (reloaded when they change)
and speaks HTTP/2 (h2, and h2c for load balancers); paths are normalized before routing, so a prefix route can't
be bypassed with `/a/../b`; the client IP comes from `X-Forwarded-For` only through trusted proxies; filters set
headers, answer CORS and compress responses; routes redirect; `php_workers` serves the files of `public/` itself
and a `static_files` cluster serves whole directories; PHP workers stream responses (generators, early hints); and
an `http_proxy` cluster forwards to remote endpoints with load balancing, health checks and retries.

Narrowed by ADR-0008 before the merge: kest is an application server behind a proxy, not "the edge". The files of
`public/` come from a `static` filter on the route to PHP, filters can be set on virtual hosts and routes, and
`http_proxy` is gone (D12).

Exit criteria:
- `curl --http2 https://…` reaches `examples/php-hello` through a TLS listener, and h2c with prior knowledge works
  on a plain listener; a renewed certificate is served without a restart.
- A deny route on `/admin/` can't be bypassed with dot segments, duplicate slashes or `%2e%2e`.
- A PHP generator streams chunk by chunk; a client that disconnects stops the stream, and the worker serves the
  next request.
- ~~`http_proxy` spreads requests over its endpoints, skips one that fails its health checks, and retries a
  connect failure on another endpoint.~~ Dropped with `http_proxy` (D12). Instead: a route with the `static` filter
  serves the files of `public/` without PHP; other routes and virtual hosts don't.
- `make check`, `make test-integration` and `make test-matrix` are green.

## Design notes

**D1. TLS listeners** (K-080, schema). A listener gets an optional `tls` block:

```yaml
listeners:
  - name: https
    address: 0.0.0.0:8443
    tls:
      certificates:                       # at least one; picked by SNI, the first one by default
        - cert_file: certs/example.com.pem  # PEM, leaf first, then the chain; relative to the config file
          key_file: certs/example.com.key
      min_version: "1.2"                  # 1.2 (default) | 1.3
    http: { … }
```

Certificates are loaded when the graph is built, so `kest validate` reports a missing or broken file with its YAML
path. A running listener checks the files every 10 seconds (size and mtime, no new dependency) and loads a changed
pair once it holds still for one check; a pair that doesn't load is logged and the old certificate stays. So
`certbot renew` needs no hook. Cipher suites stay Go's defaults. Client certificates (mTLS) go to the backlog
(K-091). ACME stays in the icebox (K-906).

**D2. HTTP/2** (K-081, schema). `listeners[].http.protocols`: a list of `http1` and `http2`. Default: both with
`tls`, `[http1]` without. `http2` on a listener without TLS means h2c with prior knowledge (what load balancers
send; Go's `http.Protocols`, no `Upgrade: h2c`). Go's HTTP/2 server limits (250 concurrent streams per
connection) stay as they are. PHP sees `protocol: HTTP/2.0`, as with RoadRunner.

**D3. Path normalization** (K-086, schema). Two options on `listeners[].http`, both on by default:
`normalize_path` (remove `.` and `..` segments, RFC 3986 § 5.2.4) and `merge_slashes` (collapse `//`). They run on
the decoded path (`%2e%2e` counts as `..`, `%2F` as `/`) before the filters and the router. When they change the
path, the request URL is rewritten to the result, so the router, the filters and the cluster agree on it; an
unchanged path keeps its original encoding (an encoded `%2F` reaches PHP as sent). A path that doesn't start with
`/` (`OPTIONS *`) is left alone.

**D4. Client IP through trusted proxies** (K-087, schema). A filter, `client_ip`:

```yaml
- type: client_ip
  config:
    trusted_proxies: [10.0.0.0/8, 127.0.0.1]  # required: CIDRs or addresses of the peers allowed to set the client IP
    header: X-Forwarded-For                   # default; or Forwarded, or one-address headers like X-Real-Ip
```

When the peer is trusted, kest walks the header from the right and skips trusted addresses: the first untrusted one
is the client (if all are trusted, the leftmost). A client can't spoof it by sending its own `X-Forwarded-For`,
which RoadRunner's `proxy_ip_parser` allows (it takes the leftmost value; checked in its v6.0.0 source). The
result goes to `httpx.RequestInfo.ClientIP`, which every consumer reads whatever the filter order: PHP's
`remoteAddr` (replaced, as RoadRunner does) and `access_log`'s `remote`. (`http_proxy` appends the peer to the
incoming `X-Forwarded-For` instead; see the log.) A trusted peer's `X-Forwarded-Proto: https` (a single `http` or `https` value) also makes the
URI that PHP sees `https://`, so frameworks build the right links behind a TLS-terminating balancer.

**D5. Headers filter** (K-084, schema). `headers` with `request` and `response` blocks, each with `set` (replace),
`add` (append), `set_if_absent` and `remove`. Literal values; framing and hop-by-hop headers (`Host`,
`Content-Length`, `Transfer-Encoding`, `Connection`, …) are rejected. Response headers are applied when the status
line is written, so they reach responses from clusters and from other filters alike.

**D6. CORS filter** (K-089, schema). `cors` with `allow_origins` (required; exact origins, `https://*.example.com`
wildcards, or `*`), `allow_methods` (default `GET, HEAD, POST`), `allow_headers`, `expose_headers`,
`allow_credentials` (default `false`; with `*` in `allow_origins` it is a config error), `max_age` (default `5m`).
kest answers preflight requests itself (`204`, the CORS headers only for an allowed origin) and adds
`Access-Control-Allow-Origin` and `Vary: Origin` to the other responses for allowed origins. Use either this
filter or the application's CORS handling, not both.

**D7. Redirect action** (K-090, schema). A third route action, `redirect: {scheme, host, port, path,
prefix_rewrite, strip_query, status}`: unset parts keep the request's; a new `scheme` or `host` drops the port unless
`port` is set; `prefix_rewrite` replaces the matched prefix (only with `match.prefix`); `status` is `301` (default),
`302`, `303`, `307` or `308`. `redirect: {scheme: https}` on a plain listener sends everything to HTTPS.

**D8. Compression** (K-083, schema). A `compression` filter with gzip only: the standard library has no brotli or
zstd encoder, and each would be a new dependency (backlog K-092). Options: `level` (1–9, default 5), `min_length`
(default `1KB`; a body with a smaller `Content-Length` isn't compressed), `content_types` (default: `text/*`, JSON,
JavaScript, XML, SVG, WASM; entries are media types or `type/*`). A response is compressed when the client accepts
gzip, the response has no `Content-Encoding`, isn't `HEAD`, `204`, `304` or `206`, and has no `Cache-Control:
no-transform`; then `Content-Length` is dropped, `Vary: Accept-Encoding` added and a strong `ETag` made weak.
Flushes reach the client (streams stay streams). Writers are pooled.

**D9. Static files** (K-082, schema). _Superseded by D12._ One implementation (`internal/staticfiles`), two uses:
- `php_workers.static: {dir: public, forbid: [.php], headers: {}}`: a `GET` or `HEAD` for a regular file under
  `dir` is served by kest, everything else goes to PHP (nginx's `try_files $uri /index.php`, RoadRunner's
  `http.static`). Off unless `dir` is set.
- a `static_files` cluster type: `{dir, index: index.html, forbid: [], headers: {}}`; a missing file is `404`.

Files open through `os.Root`, so neither `..` nor a symlink leaves `dir`. Paths with a segment that starts with a
dot are not served (`.env`, `.git/`), except under `/.well-known/`. Range requests, `If-Modified-Since` and
`If-None-Match` work (`http.ServeContent`, a weak ETag from size and mtime). `headers` are added to every file
response (`Cache-Control`). Precompressed `.gz` siblings go to the backlog (K-093). Being clusters (or part of
one), static responses are routed, counted in the metrics and logged like the others.

**D10. Streaming responses** (K-088, no schema). Verified in the sources of RoadRunner's pool v2.0.0-beta.1 and
`roadrunner/worker` 3.8.2 / `roadrunner/http` 4.2.1: a frame with the `STREAM` bit (byte 10, `0x01`) means more
frames follow; every frame repeats status and headers; a `PING` bit asks for a `PONG` frame; the server stops a
stream with a `STOP` frame and discards frames until one without `STREAM`. kest:
- `worker.Pool.ExecStream` returns the first frame and a `Stream`; the slot goroutine reads the next frames, answers
  pings and hands frames over through a small buffer. `Close` (client gone, write error, timeout) sends `STOP` and
  drains the rest. `exec_ttl` bounds each wait for a frame, as in RoadRunner, so a long stream lives as long as the
  route timeout allows (`timeout: 0s` for endless streams).
- `php_workers` writes 1xx frames (103 Early Hints) as informational responses, takes status and headers from the
  first final frame, sends each body chunk as it comes and flushes. An error after the headers went out aborts the
  connection, so the client sees a cut-off response. A non-streamed response is written as today.

**D11. `http_proxy` cluster** (K-085, schema). _Superseded by D12: built, then removed._ The second cluster type,
on `httputil.ReverseProxy`:

```yaml
- name: api
  type: http_proxy
  config:
    endpoints: [http://10.0.0.1:8080, https://api.internal:8443]   # scheme://host:port
    load_balancing: round_robin       # round_robin | least_request | random
    host: ""                          # Host sent upstream; default: the client's
    connect_timeout: 5s
    max_connections: 1024             # per endpoint
    max_idle_connections: 64          # per endpoint
    idle_timeout: 90s
    http2: false                      # h2 (ALPN) to https endpoints, h2c to http ones
    tls: { ca_file: "", server_name: "", insecure_skip_verify: false }
    health_check: { path: "", interval: 10s, timeout: 2s, healthy_threshold: 2, unhealthy_threshold: 3 }
    retries: { count: 1, on: [connect_failure] }   # connect_failure | gateway_error | reset
```

Health checks are off unless `path` is set: `GET path`, 2xx and 3xx are healthy. When no endpoint is healthy, all
are used (Envoy's panic mode) rather than failing every request. Retries go to another endpoint; a connect failure
is retried for any request (nothing was sent), `gateway_error` (502/503/504) and `reset` only for requests without
a body and with an idempotent method. Errors: connect failure `503`, timeout `504`, others `502`.
`X-Forwarded-For` (with the client IP of D4), `X-Forwarded-Host` and `X-Forwarded-Proto` are set. WebSockets pass
through (`ReverseProxy` handles `Upgrade`). `/ready` doesn't depend on upstream health (an upstream outage must not
take every kest instance out of the balancer); `/clusters` shows each endpoint's health and active requests. New
metrics: `kest_upstream_requests_total{cluster,endpoint,code}`, `kest_upstream_errors_total{cluster,endpoint,
reason}`, `kest_upstream_retries_total{cluster}`, `kest_upstream_endpoint_healthy{cluster,endpoint}`. Q-D is
answered: the cluster interface stays `http.Handler`.

**D12. Narrowing after the user's look** (ADR-0008; K-096, K-082, K-085; schema). kest stands behind a proxy:
- `http_proxy` is removed with its metrics and docs; the cluster interface stays `http.Handler`.
- Filters on virtual hosts and routes (K-096): `virtual_hosts[].filters` run for every request to the virtual host
  the router chose, `routes[].filters` after them for the route it chose, then the action. The router instruments
  each virtual host; a route's cluster marks the request in `RequestInfo`, so a response that a filter makes counts
  with `cluster=""`, and there is no lookup per request.
- The `static` filter (`internal/filters/static`) replaces `php_workers.static` and the `static_files` cluster:
  `{dir, forbid: [.php], headers}`, the file for a `GET` or `HEAD` if there is one, else the rest of the chain. Set
  on the route to PHP, it can't serve past a deny route or on another application's virtual host. `dir` is checked
  when the graph is built, so `kest validate` reports it. Directory index files (`index`) went with the cluster.

**Other choices.**
- Response wrappers (headers, compression, CORS) keep `Flush`, `Unwrap` and `http.ResponseController` working, so
  streams and `Upgrade` pass through them.
- New filters live in `internal/filters/<name>/`, the new cluster types in `internal/cluster/<name>/`.

## Steps

Steps 1–2 are the vertical slice: `curl --http2 -k https://localhost:8443/` against `php-hello`.

1. [x] **TLS listeners** (K-080) — acceptance: e2e over TLS with a generated certificate; a replaced certificate is
   served without a restart; `kest validate` locates a missing file.
2. [x] **HTTP/2** (K-081) — acceptance: e2e with h2 over TLS and h2c; PHP sees `HTTP/2.0`.
3. [x] **Path normalization** (K-086) — acceptance: unit tests for dot segments, `%2e`, `//`; e2e: a deny route
   can't be bypassed.
4. [x] **`client_ip`** (K-087) — acceptance: spoofed `X-Forwarded-For` from an untrusted peer is ignored; PHP's
   `REMOTE_ADDR` and the access log show the client.
5. [x] **`headers`** (K-084).
6. [x] **`cors`** (K-089) — acceptance: preflight answered by kest; disallowed origins get no CORS headers.
7. [x] **Redirect action** (K-090).
8. [x] **`compression`** (K-083) — acceptance: gzip round trip, streams flushed, HEAD/304/no-transform skipped.
9. [x] **Static files** (K-082) — acceptance: `php_workers.static` serves `public/` files with Range and ETag,
   PHP gets the rest, no escape from `dir`.
10. [x] **Streaming responses** (K-088) — acceptance: PHP integration test with a generator and with 103 Early
    Hints; a client that disconnects mid-stream stops it and the worker serves the next request.
11. [x] ~~**`http_proxy`** (K-085) — acceptance: e2e against test backends: balancing, health checks, retries,
    WebSocket upgrade.~~ Removed in step 13.
12. [x] **Docs and review** — `docs/` (config reference, operations: TLS and proxies), README, architecture; the
    docs review (`review.md`). No PR until the user has looked at the branch.
13. [x] **Narrowing (D12, ADR-0008)** — remove `http_proxy` (K-085); filters on virtual hosts and routes (K-096);
    the `static` filter (K-082); docs, roadmap (M5 routing, filters and pools; M7 protocols), the docs review.

## Risks / open questions

- Path normalization on by default changes what PHP sees for unusual paths (`//a`, `/a/./b`); both options can be
  turned off.
- Streaming touches the pool's slot loop, the most delicate code in kest; the fake worker gets a streaming mode so
  the pool tests (synctest) cover it without PHP.
- `http_proxy` is a large item; if it grows, passive health checks (outlier detection) and per-route retry policies
  go to the backlog.

## Log

- 2026-10-25 — Step 13 done (D12). The user looked at the branch and found kest drifting towards nginx and Envoy:
  static files inside a worker pool, a reverse proxy nobody needs behind nginx. ADR-0008 (accepted) draws the
  line; `http_proxy` and `static_files` are removed, `static` is a filter, filters work on virtual hosts and routes.
  The PHP example's `/robots.txt` now counts without a cluster. HTTP/2 and h2c stay core (nginx's `grpc_pass`,
  gRPC in M7); TLS and gzip stay as they are. K-095 moved to M5. After the narrowing `make check`,
  `make test-integration` and `make test-matrix` (PHP 8.3.36, 8.4.27, 8.5.12) are green; the docs review found 6
  drifts, all fixed. The user asked whether filters should stay on listeners only, with include/exclude paths in
  `static`; kept nested, since the router then scopes every filter by host and path in one place. PR #3, merged
  on the user's go-ahead with D1–D8 and D12 as proposed.

- 2026-10-25 — Step 12 done: README, getting started (RoadRunner mapping), operations (TLS, load balancers),
  architecture; the docs review found 19 drifts (one blocker: `docs/config.md` still said streaming was
  unsupported), all fixed except the glossary of `context.md`, which changes only with the user (Q11). Exit
  criteria met: `make check`, `make test-integration` and `make test-matrix` (PHP 8.3.36, 8.4.27, 8.5.12) green.
  A server test runs a stream with early hints through `recovery`, `headers` and `compression`.
- 2026-10-25 — Deviation from D4/D11: `http_proxy` doesn't put `RequestInfo.ClientIP` into `X-Forwarded-For`; it
  appends the peer's address to the incoming header, as every proxy in a chain does (`SetXForwarded`), so the
  upstream can walk the chain with its own trusted proxies. `X-Forwarded-Proto` does use `httpx.Scheme`.
- 2026-10-25 — Step 11 (K-085) done as in D11; beyond it, `kest_upstream_requests_active`. `registry.Build` now
  locates a `*config.FieldError` that `Build` returns (an unreadable `tls.ca_file`). Passive health checks went to
  the backlog (K-094); counting filter-answered requests too (K-095).
- 2026-10-25 — Step 10 (K-088) done as in D10. The end of a stream is `ok == false`, not `io.EOF`: a
  `*ProcessError` of a worker that died mid-stream wraps `io.EOF` and would read as a normal end. `Exec` joins a
  stream up to `MaxFrameSize` (`ErrStreamTooLarge`). The fake worker reads frames in the background, as an OS pipe
  buffers them, so that kest's PONG and STOP never block it.
- 2026-10-25 — Step 9 (K-082) done as in D9 except for `os.Root`: it refuses absolute symlinks even inside the
  directory, and Laravel's `php artisan storage:link` makes `public/storage` an absolute link out of `public/`.
  Files are opened by name instead, symlinks followed as nginx does; `..` can't occur, since no segment that starts
  with a dot is served. The cluster package is `internal/cluster/static` (type `static_files`).
- 2026-10-25 — Step 8 (K-083) done as in D8. The header goes out on the first write, so a missing Content-Type is
  sniffed from the plain body (net/http would sniff the gzipped bytes).
- 2026-10-25 — Step 7 (K-090) done as in D7. The request's scheme comes from `httpx.Scheme` (trusted proxies
  included); a redirect needs at least one part that changes.
- 2026-10-25 — Step 6 (K-089) done as in D6. `allow_headers: ["*"]` echoes the requested headers (works with
  credentials too).
- 2026-10-25 — Step 5 (K-084) done as in D5; `remove: [Date]` suppresses net/http's `Date`.
- 2026-10-25 — Step 4 (K-087) done as in D4. `httpx.IsToken` is shared by the filters that check header names.
- 2026-10-25 — Step 3 (K-086) done as in D3.
- 2026-10-25 — Step 2 (K-081) done as in D2: the vertical slice runs (`php-hello` over h2 and h2c). `php-hello`'s
  `/info` reports the protocol version.
- 2026-10-25 — Step 1 (K-080) done as in D1. Beyond D1: the metrics `kest_tls_certificate_expiry_timestamp_seconds`
  and `kest_tls_handshake_errors_total`; failed handshakes go to the debug log. `config.Config.ErrorAt` locates
  errors found while the graph is built (unreadable certificate files).
- 2026-10-25 — Draft, written while the user was away; K-084 split into K-084 (headers), K-089 (CORS), K-090
  (redirect). Research for D4 and D10 done in the RoadRunner sources (proxy_ip_parser v6.0.0, pool v2.0.0-beta.1,
  `roadrunner/worker` 3.8.2, `roadrunner/http` 4.2.1).

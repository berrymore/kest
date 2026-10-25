# Configuration reference

kest reads one YAML file (`kest serve -c kest.yaml`). This page describes every option that exists
today; [Getting started](getting-started.md) walks through a first PHP app. Check a file without starting anything with `kest validate -c kest.yaml`; add `-dump` to print
the effective configuration with all defaults filled in. The dump shows values after environment
expansion, so it can include secrets.

```yaml
version: v1

log:
  level: info
  format: text

admin:
  address: 127.0.0.1:9901

server:
  drain_timeout: 15s

listeners:
  - name: main
    address: 127.0.0.1:8080
    http:
      filters: []
      virtual_hosts:
        - name: default
          domains: ["*"]
          routes:
            - match: { path: /ping }
              direct_response: { status: 200, body: "pong" }
            - match: { prefix: / }
              route: { cluster: app, timeout: 30s }

clusters:
  - name: app
    type: php_workers
    config:
      command: [php, worker.php]
```

## General rules

- **Strict.** Unknown fields are errors, at every level.
- **All errors at once.** Validation reports every problem it finds, sorted by position, as
  `file:line:column: path: message`:

  ```
  kest.yaml:13:32: listeners[0].http.virtual_hosts[0].routes[0].route.cluster: unknown cluster "nope"
  ```

- **Names** (listeners, virtual hosts, clusters) are 1–64 characters: letters, digits, `_`, `.`, `-`,
  starting with a letter or digit. They must be unique within their kind.
- **Durations** are Go duration strings: `500ms`, `30s`, `1m30s`. A bare number such as `30` is
  rejected, since the unit would be ambiguous.
- **Sizes** are integers with an optional unit: `512`, `64KB`, `128MB`, `1GiB`. Every unit is a power
  of 1024, as in PHP's `memory_limit`: `K`/`KB`/`KiB`, `M`/`MB`/`MiB`, `G`/`GB`/`GiB`, `T`/`TB`/`TiB`.
- **Addresses** are `host:port` with a numeric port: `127.0.0.1:8080`, `:8080`, `[::1]:8080`.
  Port `0` picks any free port.

## Environment variables

Scalar values may reference environment variables:

| Syntax | Result |
|---|---|
| `${NAME}` | value of `NAME`; an error if it is not set |
| `${NAME:-default}` | value of `NAME`, or `default` if it is unset or empty |
| `$${NAME}` | the literal text `${NAME}` |

Any other `$` is kept as is (`pa$$word` needs no escaping). Mapping keys are never expanded.

Expansion happens after the YAML is parsed, so a value can't inject YAML structure. An unquoted
reference is typed after expansion: `workers: ${WORKERS}` becomes a number. A quoted reference stays a
string, as YAML says. Inside flow mappings (`{ ... }`), `}` ends the mapping, so there a reference has
to be quoted. Use block style for references that must become numbers or booleans:

```yaml
direct_response:
  status: ${STATUS}        # a number
address: "${HOST}:8080"    # a string
```

## `version`

Required. Must be `v1`.

## `log`

| Field | Default | Values |
|---|---|---|
| `level` | `info` | `debug`, `info`, `warn`, `error` |
| `format` | `text` | `text`, `json` |

Logs go to stderr.

## `admin`

The admin HTTP server, for probes and introspection.

| Field | Default | |
|---|---|---|
| `address` | `127.0.0.1:9901` | Must differ from every listener address |

Endpoints:

| Endpoint | |
|---|---|
| `GET /` | List of endpoints |
| `GET /ready` | `200 ready` while serving traffic; `503 starting` / `503 draining` otherwise, and `503 not serving: <clusters>` while a cluster has no live worker (a pool whose workers are only being replaced still counts as serving) |
| `GET /server_info` | JSON: version, commit, Go version, state, start time, uptime, config file |
| `GET /config_dump` | The effective configuration as YAML, with every default filled in, like `kest validate -dump`, but with the values of every `env` map shown as `[redacted]` (`validate -dump` shows them) |
| `GET /clusters` | JSON: every cluster with its `name`, `type`, whether it is `serving` (only for types that can be down: `php_workers`), and its `status`: for `php_workers` the pool's `health` (`healthy`, `degraded`, `unhealthy`), `queue_length`, and each worker's `slot`, `state`, `pid`, `jobs`, `started_at`, `age_seconds` and `memory_bytes` (when sampled) |
| `POST /clusters/{name}/reset` | Replace the cluster's workers one by one (a rolling restart, to deploy new code), and answer when done: `200 reset done`; `404` for an unknown cluster, `409` while a reset runs, `501` for a cluster type that can't, `503` while starting or draining, `500` with the reason when a new worker failed to boot (the reset stops there; the workers it didn't reach keep serving). A client that disconnects doesn't stop the reset. |
| `GET /metrics` | Metrics in the Prometheus text format; see [Metrics](metrics.md) |

The admin API has no authentication. Keep it on a loopback or private address.

## `server`

| Field | Default | |
|---|---|---|
| `drain_delay` | `0s` | After SIGINT/SIGTERM, how long the listeners keep serving new requests while `/ready` already answers `503 draining`, so that load balancers stop sending traffic before connections close. Set it to a bit more than the load balancer's health check interval. |
| `drain_timeout` | `15s` | After SIGINT/SIGTERM (and the `drain_delay`), how long in-flight requests may take to finish before their connections are closed. The same budget then applies to stopping clusters. `0s` closes immediately. |

Shutdown order: `/ready` switches to `503 draining`; the `drain_delay` passes; listeners stop accepting and drain; clusters stop;
the admin server stops last. A second SIGINT/SIGTERM terminates the process immediately. SIGHUP resets
the workers of every cluster, like `POST /clusters/{name}/reset`, and logs the outcome; the configuration is
not reloaded.

## `listeners`

At least one listener is required.

| Field | Required | |
|---|---|---|
| `name` | yes | Unique listener name |
| `address` | yes | `host:port` to bind; must be unique (except port `0`) |
| `tls` | no | Serve HTTPS with these certificates; see below |
| `http` | yes | HTTP is the only protocol so far |

### `listeners[].tls`

```yaml
listeners:
  - name: https
    address: 0.0.0.0:443
    tls:
      certificates:
        - cert_file: /etc/letsencrypt/live/example.com/fullchain.pem
          key_file: /etc/letsencrypt/live/example.com/privkey.pem
        - cert_file: certs/other.example.pem   # relative to this file
          key_file: certs/other.example.key
    http: { … }
```

| Field | Default | |
|---|---|---|
| `certificates` | — | Required, at least one. The client's server name (SNI) picks the certificate; a client that asks for none of them gets the first. |
| `certificates[].cert_file` | — | Required. PEM: the certificate, then the intermediates. |
| `certificates[].key_file` | — | Required. PEM: its private key. |
| `min_version` | `"1.2"` | The oldest TLS version accepted: `"1.2"` or `"1.3"`. |

The files are read when the config is loaded, so `kest validate` reports one that is missing or broken. While kest
runs, it checks them every 10 seconds and loads a pair that changed once the change has held still for a check:
a renewed certificate (`certbot renew`) is served without a restart or a reload hook. A pair that fails to load is
logged as an error and the old certificate stays. Cipher suites are Go's defaults. Failed handshakes are counted
in `kest_tls_handshake_errors_total` and logged at `debug` level, since scanners cause them all the time.


### `listeners[].http`

| Field | Default | |
|---|---|---|
| `filters` | none | HTTP filters, run in order before the router, for every request of the listener. Each is `{type, config}`; see [Filters](#filters). |
| `protocols` | `[http1, http2]` with `tls`, `[http1]` without | The HTTP versions served. `http2` over TLS is negotiated with ALPN (h2); on a listener without `tls` it is h2c with prior knowledge, which load balancers and proxies use towards backends (not the `Upgrade: h2c` dance). PHP sees the version as the request's protocol (`2` in PSR-7). |
| `normalize_path` | `true` | Remove `.` and `..` segments from the request path before the filters and the router see it (RFC 3986), so that `/public/../admin/` can't slip past a route for `/admin/`. `%2e%2e` counts as `..` and `%2F` as `/`, since paths are matched decoded. A path that changes reaches the cluster in its normalized form; an unchanged one keeps its original encoding. |
| `merge_slashes` | `true` | Collapse repeated slashes: `//admin//x` is `/admin/x`. |
| `read_header_timeout` | `10s` | How long a client may take to send the request line and headers; then the connection is closed. Protects against slow clients. `0s` turns it off. |
| `read_timeout` | `0s` (off) | How long reading a whole request may take, body included. Off by default, since uploads on slow links take long; `php_workers` stops reading a body at the route `timeout` anyway (`408`). |
| `write_timeout` | `0s` (off) | How long writing a response may take, counted from the end of the request headers. |
| `idle_timeout` | `120s` | How long a keep-alive connection may wait for its next request. `0s`: no limit. |
| `max_header_bytes` | `1MB` | Request line and headers together, 1KB–64MB; larger requests get `431`. |
| `virtual_hosts` | — | Required, at least one |

### Filters

Filters can be set in three places, and run in this order: those of the listener (`http.filters`), for every
request; those of the virtual host the router chose (`virtual_hosts[].filters`); those of the route it chose
(`routes[].filters`); then the route's action. A filter may answer a request itself instead of passing it on (CORS
preflights, files of `static`): the rest of the chain and the action don't run. In the [metrics](metrics.md), such
an answer from a virtual host's or a route's filter counts with an empty `cluster`; one from a listener's filter
isn't counted (it never reaches the router).

Built-in types: `request_id`, `access_log`, `recovery`, `client_ip`, `headers`, `cors`, `compression`, `static`. Put
`recovery` first so that it covers the other filters too:

```yaml
http:
  filters:
    - type: recovery
    - type: request_id
    - type: access_log
  virtual_hosts:
    - name: api
      domains: [api.example.com]
      filters:
        - type: cors          # only for this virtual host
          config: {allow_origins: [https://app.example.com]}
      routes:
        - match: {prefix: /}
          route: {cluster: api}
```

Every filter works in any of the three places. Each place builds its own instance from its config.

**`request_id`** gives every request an ID: in a request header that the cluster sees (PHP reads it as
`$request->getHeaderLine('X-Request-Id')`), in the same header of the response, and in the access log. A new ID is
32 random hex digits.

| Field | Default | |
|---|---|---|
| `header` | `X-Request-Id` | The header that carries the ID. |
| `trust_incoming` | `false` | Keep an ID that the client sent, if it is 1–128 visible ASCII characters. Turn it on only behind a proxy that sets the header itself: otherwise clients choose their IDs. |

**`access_log`** writes one line per request to kest's log (stderr, in `log.format`), with `component=access`
and the listener's name: `method`, `path` (without the query string, which may hold secrets), `proto`, `status`,
`bytes` (of the body), `duration`, `remote` (the client's IP, from `client_ip` when a trusted proxy reported it),
`request_id`, `virtual_host`, `route` (the route's `name`), `cluster` and `user_agent`. It takes no config.

**`recovery`** turns a panic in kest below it into a `500` with the stack in the log, instead of a dropped
connection. If the response had started, the connection is cut, so that the client doesn't mistake a partial
response for a complete one. It takes no config.

**`client_ip`** takes the client's IP from a forwarding header, when kest runs behind a load balancer or a proxy,
but only from peers listed in `trusted_proxies`:

```yaml
- type: client_ip
  config:
    trusted_proxies: [10.0.0.0/8, 192.168.0.0/16, 127.0.0.1]
    header: X-Forwarded-For
```

| Field | Default | |
|---|---|---|
| `trusted_proxies` | — | Required. The addresses (`192.0.2.10`, `2001:db8::1`) and networks (`10.0.0.0/8`) of the proxies allowed to tell the client's IP. |
| `header` | `X-Forwarded-For` | Where the IP is: `X-Forwarded-For`, `Forwarded` (its `for=` parameters), or a header with one address set by the proxy, such as `X-Real-Ip` or `CF-Connecting-IP`. |

When a request comes from a trusted proxy, kest reads the list from the end: the first address that isn't a trusted
proxy is the client (if all of them are, the first one is). So a client can't choose its IP by sending
`X-Forwarded-For` itself, which RoadRunner's `proxy_ip_parser` allows: it takes the first address. An entry that
isn't an IP ends the search. A trusted proxy's `X-Forwarded-Proto: https` (one value, `http` or `https`) also sets
the scheme. PHP sees the client's IP as the remote address (`REMOTE_ADDR`), as with RoadRunner, and the scheme in
the request URI, so frameworks build `https://` links behind a TLS-terminating balancer. `access_log` logs the
client's IP whatever the filter order. Requests from other peers keep the peer's address and their headers are
ignored, not removed.

**`headers`** changes request headers on the way to the cluster and response headers on the way to the client:

```yaml
- type: headers
  config:
    request:
      set: {X-Env: prod}
      remove: [X-Debug]
    response:
      set_if_absent:
        Strict-Transport-Security: max-age=63072000
        X-Content-Type-Options: nosniff
      remove: [X-Powered-By]
```

| Field | |
|---|---|
| `request` | Rules for the request that the cluster gets. |
| `response` | Rules for every response: from clusters, direct responses, redirects and kest's own errors. |
| `remove` | Header names to remove. |
| `set` | Name → value: replaces any value the header had. |
| `add` | Name → value: adds a value to those the header has. |
| `set_if_absent` | Name → value: only when the header has no value, so that the application can override it. |

The rules run in this order: `remove`, `set`, `add`, `set_if_absent`. Values are literal. Headers that kest
manages can't be changed: `Host`, `Content-Length`, `Transfer-Encoding`, `Connection`, `Keep-Alive`,
`Proxy-Connection`, `TE`, `Trailer`, `Upgrade`. `response.remove: [Date]` stops kest from adding a `Date`.

**`cors`** lets pages from other origins call the application from a browser (Cross-Origin Resource Sharing):

```yaml
- type: cors
  config:
    allow_origins: ["https://app.example.com", "https://*.example.com"]
    allow_methods: [GET, POST, PUT, DELETE]
    allow_headers: [Content-Type, Authorization]
    allow_credentials: true
```

| Field | Default | |
|---|---|---|
| `allow_origins` | — | Required. Exact origins (`https://app.example.com`, with the port if not the default), subdomain wildcards (`https://*.example.com` matches `https://a.example.com` but not `https://example.com`), or `"*"` for any origin. |
| `allow_methods` | `[GET, HEAD, POST]` | Methods allowed in cross-origin requests. |
| `allow_headers` | none | Request headers allowed beyond the safelisted ones (`Content-Type` with a JSON body needs listing); `"*"` allows any. |
| `expose_headers` | none | Response headers that scripts may read beyond the safelisted ones. |
| `allow_credentials` | `false` | Let requests carry cookies and HTTP authentication. Can't be combined with `"*"` in `allow_origins`. |
| `max_age` | `5m` | How long browsers may cache a preflight answer. `0s` leaves it to the browser. |

kest answers preflight requests (`OPTIONS` with `Origin` and `Access-Control-Request-Method`) itself with `204`;
they don't reach the cluster. The CORS headers are there only if the origin, the method and every requested header
are allowed, so the browser blocks the rest. Other requests from an allowed origin get
`Access-Control-Allow-Origin` (and `-Credentials`, `-Expose-Headers`), replacing any the application set; with a
list of origins, every response gets `Vary: Origin` so that caches keep them apart. Use either this filter or the
application's CORS handling (such as `nelmio/cors-bundle`), not both.

**`compression`** gzips responses of compressible types for clients that accept it (`Accept-Encoding: gzip`):

```yaml
- type: compression
  config:
    level: 5
```

| Field | Default | |
|---|---|---|
| `level` | `5` | gzip level, from `1` (fastest) to `9` (smallest). |
| `min_length` | `1KB` | A body whose `Content-Length` is smaller isn't compressed. A body without one (a stream) is. |
| `content_types` | `text/*`, `application/javascript`, `application/json`, `application/xml`, `application/ld+json`, `application/manifest+json`, `application/problem+json`, `application/rss+xml`, `application/atom+xml`, `application/xhtml+xml`, `image/svg+xml`, `application/wasm` | Media types to compress: `type/subtype`, or `type/*` for a whole type. Images, video and archives are compressed already. |

A response isn't compressed when it already has a `Content-Encoding`, is a `HEAD`, `204`, `206` or `304`, or says
`Cache-Control: no-transform`. A compressed one loses its `Content-Length`, gets `Content-Encoding: gzip` and a weak
`ETag`; every compressible response gets `Vary: Accept-Encoding`. Flushes go through, so streamed responses stay
streams. Only gzip: brotli and zstd would need dependencies.

**`static`** serves the files of your public directory without PHP, as nginx's `try_files $uri @app` or RoadRunner's
`http.static` do: a `GET` or `HEAD` for a file in `dir` is answered by kest; anything else, missing files included,
goes on to the route's action. Set it on the route that goes to PHP, so that it doesn't answer on other routes or
virtual hosts:

```yaml
routes:
  - match: {prefix: /admin/}
    direct_response: {status: 403}     # no file of public/admin/ either
  - match: {prefix: /}
    filters:
      - type: static
        config:
          dir: public
          headers: {Cache-Control: "public, max-age=3600"}
    route: {cluster: app}
```

| Field | Default | |
|---|---|---|
| `dir` | — | Required. The directory served; relative to the config file. `kest validate` fails if it isn't a directory. |
| `forbid` | `[.php]` | Extensions never served, so the source of `index.php` doesn't leak; such requests go on. |
| `headers` | none | Headers added to every file served (`{Name: value}`), such as `Cache-Control`. |

Directories aren't served (`/` goes on), nor are paths with a segment that starts with a dot (`.env`, `.git/`),
except under `/.well-known/`. Symbolic links are followed, so Laravel's `public/storage` works. Responses support
`Range`, `If-Modified-Since` and `If-None-Match` (a weak `ETag` from the file's size and modification time); the
`Content-Type` comes from the extension. On a route, a file counts in the metrics without a cluster, so the PHP
cluster's latency is PHP's. When the proxy in front of kest can see the files, let it serve them instead.

### `virtual_hosts[]`

| Field | Required | |
|---|---|---|
| `name` | yes | Unique within the listener |
| `domains` | yes | Hosts this virtual host serves |
| `filters` | no | Filters for every request to this virtual host, after the listener's, before a route is chosen; see [Filters](#filters) |
| `routes` | yes | Tried in order; the first match wins |

A request's host is taken from the `Host` header, without the port, lowercased and without a trailing
dot. Selection order:

1. An exact domain: `example.com`.
2. The longest matching suffix wildcard: `*.example.com` matches `a.example.com` and
   `a.b.example.com`, but not `example.com`.
3. The `*` domain, if any.

No match → `404`. Domains must not include a port. A domain may appear in only one virtual host per
listener. IPv6 hosts are written in brackets: `[::1]`.

### `routes[]`

Every route has a `match` and exactly one action.

| Field | |
|---|---|
| `name` | Optional. Up to 64 letters, digits, `_`, `.` or `-`, unique in the whole config. Labels the route's requests in the metrics (`route`) and the access log. |
| `match.prefix` | The path starts with this value; must start with `/` |
| `match.path` | The path equals this value; must start with `/` |
| `filters` | Filters for the requests of this route, after the virtual host's, before the action; see [Filters](#filters) |
| `route` | Forward to a cluster |
| `direct_response` | Answer without an upstream |
| `redirect` | Answer with a redirect |

Set exactly one of `match.prefix` and `match.path`. The match runs against the decoded URL path, after
`normalize_path` and `merge_slashes`, without the query string. If no route matches → `404`.

**`route`**

| Field | Default | |
|---|---|---|
| `cluster` | — | Name of a cluster in `clusters`; required |
| `timeout` | `30s` | Bounds the whole request. The cluster answers `504` when it expires; `php_workers` answers `408` if the request body is still arriving then. `0s` disables it. |

**`direct_response`**

| Field | Default | |
|---|---|---|
| `status` | `200` | 100–599 |
| `body` | empty | Sent as `text/plain; charset=utf-8` |

**`redirect`** sends the client to the request's URL with some parts replaced:

```yaml
routes:
  - match: {prefix: /}                  # on a plain listener: everything to HTTPS
    redirect: {scheme: https}
  - match: {prefix: /blog/}
    redirect: {host: blog.example.com, prefix_rewrite: /}
```

| Field | Default | |
|---|---|---|
| `scheme` | the request's | `http` or `https` |
| `host` | the request's | A host name or an IP address, without a port |
| `port` | the request's | A new `scheme` or `host` drops the request's port unless `port` is set; the scheme's default port is left out of the URL. |
| `path` | the request's | Replaces the whole path. |
| `prefix_rewrite` | — | Replaces the part of the path that `match.prefix` matched: with `match.prefix: /blog/` and `prefix_rewrite: /`, `/blog/a` becomes `/a`. Not with `path`. |
| `strip_query` | `false` | Drop the query string; otherwise it is kept. |
| `status` | `301` | `301`, `302`, `303`, `307` or `308`. `307` and `308` keep the method and body of a `POST`. |

At least one part must change. The request's scheme is the one the client used, as the `client_ip` filter reports
it behind a trusted proxy; still, a `scheme: https` redirect on a listener that a TLS-terminating balancer sends all
traffic to would loop: let the balancer redirect then.

## `clusters`

Named upstreams that routes forward to. The `type` selects the implementation; `config` holds that
implementation's options and is validated just as strictly.

| Field | Required | |
|---|---|---|
| `name` | yes | Unique cluster name |
| `type` | yes | Implementation; see below |
| `config` | depends on the type | Type-specific options |

Built-in type: [`php_workers`](#php_workers). An unknown `type` is rejected with the list of known types.

### `php_workers`

A pool of long-lived PHP worker processes that speak the RoadRunner protocol: an unmodified
`roadrunner/http` (formerly `spiral/roadrunner-http`) `PSR7Worker` or `HttpWorker` loop serves requests
behind kest as it does behind RoadRunner.

```yaml
clusters:
  - name: app
    type: php_workers
    config:
      command: [php, public/worker.php]
      env: {APP_ENV: prod}
      pool:
        workers: 8
        max_jobs: 1000
      supervisor:
        exec_ttl: 30s
        max_worker_memory: 256MB
```

| Field | Default | |
|---|---|---|
| `command` | — | Required. The worker executable and its arguments; `php` is looked up in `PATH`. |
| `dir` | the config file's directory | Working directory of the workers. A relative path is relative to the config file. |
| `env` | none | Variables added to the workers' environment (`{NAME: value}`). |
| `inherit_env` | `true` | Pass kest's own environment to the workers, as RoadRunner does. With `false`, workers get only `env` and the variables below. |
| `codec` | `json` | How the request head is encoded: `json` or `proto`. See below. |
| `max_request_body` | `16MB` | Larger request bodies get `413`. A body is read into memory (files of multipart uploads go to temp files) before the request waits for a worker. RoadRunner's default is 1000 MB. |
| `max_buffered_bodies` | `256MB` | The request body bytes the cluster holds at once, at least `max_request_body`. Each request reserves its `Content-Length` (or `max_request_body` when the length is unknown) before its body is read, until its response is written; one that doesn't fit gets `503` at once. Bodies count once, though a body can be in memory twice while it goes to a worker. |
| `max_response_size` | `64MB` | A worker that sends a larger response (headers and body) is replaced, and the request gets `502`. At most `4GB`. |
| `raw_body` | `false` | Send `application/x-www-form-urlencoded` and `multipart/form-data` bodies as they are instead of parsing them (RoadRunner's `http.raw_body`). |

Every worker also gets `RR_MODE=http` and `RR_RELAY=pipes`; they override the same names from `env` or
the inherited environment.

**`uploads`**

Files of `multipart/form-data` requests are written to temp files, which PHP sees as PSR-7 uploaded
files (`$request->getUploadedFiles()`), and removed after the response.

| Field | Default | |
|---|---|---|
| `dir` | the OS temp directory | Where the temp files go; relative to the config file. |
| `forbid` | none | Extensions whose files are not stored, like `[.php, .exe]`. Case doesn't matter. |
| `allow` | none (all) | If set, only files with these extensions are stored. |

A file that isn't stored reaches PHP with an error code instead of content: `UPLOAD_ERR_EXTENSION` for
`forbid`/`allow`, `UPLOAD_ERR_NO_TMP_DIR` or `UPLOAD_ERR_CANT_WRITE` when kest can't write it (logged).
Uploads count towards `max_request_body`.

**`pool`**

| Field | Default | |
|---|---|---|
| `workers` | number of CPUs | Worker processes, 1–1024. |
| `max_jobs` | `0` (unlimited) | Requests per worker before it is replaced; each worker's limit gets up to 14 % extra, so workers don't restart together. |
| `max_queue_size` | `1000` | Requests that may wait for a free worker; more get `503` at once. |
| `allocate_timeout` | `60s` | How long a request waits for a free worker before it gets `503`. |
| `start_timeout` | `60s` | How long a new worker may take to start, including the application's boot. |
| `destroy_timeout` | `10s` | How long a worker may take to exit after it is asked to stop; then it is killed. |

**`supervisor`**

| Field | Default | |
|---|---|---|
| `ttl` | `0s` (off) | A worker's maximum lifetime; it is replaced after its current request. |
| `idle_ttl` | `0s` (off) | A worker idle this long is replaced. |
| `exec_ttl` | `60s` | A request's maximum time in a worker (per chunk when the worker streams); the worker is killed after it and the request gets `504`. `0s` disables it. |
| `max_worker_memory` | `0` (off) | A worker whose RSS grows past this size is replaced after its current request. Linux and macOS only. |
| `watch_tick` | `5s` | How often worker memory is checked. |

The route `timeout` still bounds each request: when it expires the client gets `504`, and the worker
finishes the request (within `exec_ttl`) with nobody waiting for it.

**`reload`**

For development: replace the workers when your code changes, with the same rolling reset as
`POST /clusters/{name}/reset`. kest scans the directories every `interval` (by file name, size and modification
time, no file system events), and resets once a change has held still for one scan, so that saving many files
makes one reset. Directories whose names start with a dot are skipped.

```yaml
      reload:
        dirs: [src, config]
```

| Field | Default | |
|---|---|---|
| `dirs` | none (off) | Directories to watch, recursively; relative to the config file. |
| `patterns` | `["*.php"]` | Names of the files to watch (shell patterns, like `*.yaml`). |
| `interval` | `1s` | How often to scan; at least `10ms`. |
| `max_files` | `20000` | A scan that finds more matching files stops watching, with an error in the log. |

A rewrite that keeps a file's size and lands within one tick of the file system's clock after a scan (a few
milliseconds on Linux) isn't noticed until the next change.

**Startup and shutdown.** kest starts every worker before it opens the listeners, and fails to start
if one of them can't boot (the error shows the worker's exit status and the end of its stderr).
Workers that crash later are replaced, with a growing delay (up to 30s) if they keep failing. A rolling
reset (`POST /clusters/{name}/reset`, SIGHUP) boots each replacement while the old worker keeps serving, so the
pool keeps its capacity and runs one extra process at a time. On
shutdown, running requests finish first. Worker stderr is logged line by line with `cluster` and
`worker_pid`. Anything a worker prints to stdout before `Worker::create()` (for example a PHP warning
during boot, since PHP's CLI prints errors to stdout) breaks the protocol: the worker fails to start,
and the error shows the start of what it printed.

**Streaming.** A worker may stream its response, as under RoadRunner: `HttpWorker::respond()` with a
`Generator` body, or `PSR7Worker` with `$chunkSize` set. kest sends each chunk to the client as it comes
(chunked encoding, no `Content-Length`), with the status and headers of the first frame; later frames' headers are
ignored. A frame with a 1xx status, such as `respond(103, '', ['Link' => …], false)`, goes out as an informational
response (103 Early Hints) before the final one. `supervisor.exec_ttl` bounds the wait for each chunk, not the
whole stream; the route `timeout` bounds the whole request, so give long streams (server-sent events) a route with
`timeout: 0s`. When the client goes away, kest tells the worker to stop: the generator gets a
`StreamStoppedException` (after at most five more chunks, when the worker next checks) and the worker serves the
next request. A worker that keeps streaming gets the longer of `exec_ttl` and `pool.destroy_timeout`, then it is
replaced. If a stream fails after its headers went out, the connection is cut, so the client doesn't take a partial
response for a complete one.

**Codec.** The request head (method, URI, headers, cookies, …) travels as JSON or protobuf. PHP's
`roadrunner/http` reads either and answers in the same format. `json` (the default) works with every
version of `spiral/roadrunner-http` / `roadrunner/http` from 2.0 on and costs PHP the least: about
15 µs per request, against about 20 µs for `proto` with the `protobuf` PHP extension and about 135 µs
without it ([benchmarks](benchmarks.md)). `proto` is what RoadRunner v3 sends, and needs
`spiral/roadrunner-http` 3.5 or later; use it with the extension.

**Requests.** Workers see requests as under RoadRunner: `REQUEST_URI` and the PSR-7 URI are absolute
(`http://host/path?query`), `REMOTE_ADDR` is the client IP, cookies are URL-decoded. Form bodies
(`application/x-www-form-urlencoded`, `multipart/form-data`) are parsed into the PSR-7 parsed body
with PHP's bracket syntax (`tags[]=a&tags[]=b`, `user[email]=…`); other bodies arrive raw. Unlike
RoadRunner, kest also parses form bodies of DELETE and other methods, keeps field order, and limits
forms to 10000 fields and 1000 multipart parts.

**Responses.** kest sets `Content-Length` itself and ignores the worker's `Content-Length` and
`Transfer-Encoding` (a HEAD response keeps the worker's `Content-Length`); headers named in a
`Trailer` header are sent as trailers. A streamed response is chunked instead (see Streaming above).

**Errors.** The client gets the status and its standard text; the details go to the log.

| Situation | Status |
|---|---|
| Request body over `max_request_body` | `413` |
| Request body still arriving when the route `timeout` expires | `408` |
| Request line and headers over `max_header_bytes` (from the listener) | `431` |
| Malformed form body | `400` |
| No free worker within `allocate_timeout`, queue full, `max_buffered_bodies` full, or kest shutting down | `503` |
| `exec_ttl` or the route `timeout` expired | `504` |
| The worker reported an error (`$worker->error()`) | `500` |
| The worker crashed, broke the protocol, or sent an invalid or too large response | `502` |

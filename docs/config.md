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
| `GET /clusters` | JSON: every cluster with its `name`, `type`, whether it is `serving`, and its `status`; for `php_workers` the pool's `health` (`healthy`, `degraded`, `unhealthy`), `queue_length`, and each worker's `slot`, `state`, `pid`, `jobs`, `started_at`, `age_seconds` and `memory_bytes` (when sampled) |
| `POST /clusters/{name}/reset` | Replace the cluster's workers one by one (a rolling restart, to deploy new code), and answer when done: `200 reset done`; `404` for an unknown cluster, `409` while a reset runs, `501` for a cluster type that can't, `503` while starting or draining, `500` with the reason when a new worker failed to boot (the reset stops there; the workers it didn't reach keep serving). A client that disconnects doesn't stop the reset. |
| `GET /metrics` | Metrics in the Prometheus text format; see [Metrics](metrics.md) |

The admin API has no authentication. Keep it on a loopback or private address.

## `server`

| Field | Default | |
|---|---|---|
| `drain_timeout` | `15s` | After SIGINT/SIGTERM, how long in-flight requests may take to finish before their connections are closed. The same budget then applies to stopping clusters. `0s` closes immediately. |

Shutdown order: `/ready` switches to `503 draining`; listeners stop accepting and drain; clusters stop;
the admin server stops last. A second SIGINT/SIGTERM terminates the process immediately. SIGHUP resets
the workers of every cluster, like `POST /clusters/{name}/reset`, and logs the outcome; the configuration is
not reloaded.

## `listeners`

At least one listener is required.

| Field | Required | |
|---|---|---|
| `name` | yes | Unique listener name |
| `address` | yes | `host:port` to bind; must be unique (except port `0`) |
| `http` | yes | HTTP is the only protocol so far |


### `listeners[].http`

| Field | Required | |
|---|---|---|
| `filters` | no | HTTP filters, run in order before the router. Each is `{type, config}`; see [Filters](#filters). |
| `read_header_timeout` | `10s` | How long a client may take to send the request line and headers; then the connection is closed. Protects against slow clients. `0s` turns it off. |
| `read_timeout` | `0s` (off) | How long reading a whole request may take, body included. Off by default, since uploads on slow links take long; `php_workers` stops reading a body at the route `timeout` anyway (`408`). |
| `write_timeout` | `0s` (off) | How long writing a response may take, counted from the end of the request headers. |
| `idle_timeout` | `120s` | How long a keep-alive connection may wait for its next request. `0s`: no limit. |
| `max_header_bytes` | `1MB` | Request line and headers together, 1KB–64MB; larger requests get `431`. |
| `virtual_hosts` | yes | At least one |

### Filters

Built-in types: `request_id`, `access_log`, `recovery`. Put `recovery` first so that it covers the other
filters too:

```yaml
http:
  filters:
    - type: recovery
    - type: request_id
    - type: access_log
```

**`request_id`** gives every request an ID: in a request header that the cluster sees (PHP reads it as
`$request->getHeaderLine('X-Request-Id')`), in the same header of the response, and in the access log. A new ID is
32 random hex digits.

| Field | Default | |
|---|---|---|
| `header` | `X-Request-Id` | The header that carries the ID. |
| `trust_incoming` | `false` | Keep an ID that the client sent, if it is 1–128 visible ASCII characters. Turn it on only behind a proxy that sets the header itself: otherwise clients choose their IDs. |

**`access_log`** writes one line per request to kest's log (stderr, in `log.format`), with `component=access`
and the listener's name: `method`, `path` (without the query string, which may hold secrets), `proto`, `status`,
`bytes` (of the body), `duration`, `remote` (the client's IP), `request_id`, `virtual_host`, `cluster` and
`user_agent`. It takes no config.

**`recovery`** turns a panic in kest below it into a `500` with the stack in the log, instead of a dropped
connection. If the response had started, the connection is cut, so that the client doesn't mistake a partial
response for a complete one. It takes no config.

### `virtual_hosts[]`

| Field | Required | |
|---|---|---|
| `name` | yes | Unique within the listener |
| `domains` | yes | Hosts this virtual host serves |
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
| `match.prefix` | The path starts with this value; must start with `/` |
| `match.path` | The path equals this value; must start with `/` |
| `route` | Forward to a cluster |
| `direct_response` | Answer without an upstream |

Set exactly one of `match.prefix` and `match.path`. The match runs against the URL path, without the
query string. If no route matches → `404`.

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

## `clusters`

Named upstreams that routes forward to. The `type` selects the implementation; `config` holds that
implementation's options and is validated just as strictly.

| Field | Required | |
|---|---|---|
| `name` | yes | Unique cluster name |
| `type` | yes | Implementation; see below |
| `config` | depends on the type | Type-specific options |

Built-in types: [`php_workers`](#php_workers). An unknown `type` is rejected with the list of known types.

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
| `max_request_body` | `16MB` | Larger request bodies get `413`. A body is read into memory (files of multipart uploads go to temp files) before the request waits for a worker, so up to `pool.workers + pool.max_queue_size` bodies can be held at once. RoadRunner's default is 1000 MB. |
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
| `exec_ttl` | `60s` | A request's maximum time in a worker; the worker is killed after it and the request gets `504`. `0s` disables it. |
| `max_worker_memory` | `0` (off) | A worker whose RSS grows past this size is replaced after its current request. Linux and macOS only. |
| `watch_tick` | `5s` | How often worker memory is checked. |

The route `timeout` still bounds each request: when it expires the client gets `504`, and the worker
finishes the request (within `exec_ttl`) with nobody waiting for it.

**Startup and shutdown.** kest starts every worker before it opens the listeners, and fails to start
if one of them can't boot (the error shows the worker's exit status and the end of its stderr).
Workers that crash later are replaced, with a growing delay (up to 30s) if they keep failing. A rolling
reset (`POST /clusters/{name}/reset`, SIGHUP) boots each replacement while the old worker keeps serving, so the
pool keeps its capacity and runs one extra process at a time. On
shutdown, running requests finish first. Worker stderr is logged line by line with `cluster` and
`worker_pid`. Anything a worker prints to stdout before `Worker::create()` (for example a PHP warning
during boot, since PHP's CLI prints errors to stdout) breaks the protocol: the worker fails to start,
and the error shows the start of what it printed.

**Codec.** The request head (method, URI, headers, cookies, …) travels as JSON or protobuf. PHP's
`roadrunner/http` reads either and answers in the same format. `json` (the default) works with every
version of `spiral/roadrunner-http` / `roadrunner/http` from 2.0 on and costs PHP less: about 30 µs
per request against about 140 µs for `proto` without the `protobuf` PHP extension. `proto` is what
RoadRunner v3 sends, and needs `spiral/roadrunner-http` 3.5 or later.

**Requests.** Workers see requests as under RoadRunner: `REQUEST_URI` and the PSR-7 URI are absolute
(`http://host/path?query`), `REMOTE_ADDR` is the client IP, cookies are URL-decoded. Form bodies
(`application/x-www-form-urlencoded`, `multipart/form-data`) are parsed into the PSR-7 parsed body
with PHP's bracket syntax (`tags[]=a&tags[]=b`, `user[email]=…`); other bodies arrive raw. Unlike
RoadRunner, kest also parses form bodies of DELETE and other methods, keeps field order, and limits
forms to 10000 fields and 1000 multipart parts.

**Responses.** kest sets `Content-Length` itself and ignores the worker's `Content-Length` and
`Transfer-Encoding` (a HEAD response keeps the worker's `Content-Length`); headers named in a
`Trailer` header are sent as trailers. Streamed responses (`PSR7Worker::$chunkSize`, generator bodies)
are not supported yet: a worker that streams is replaced and the request gets `502`.

**Errors.** The client gets the status and its standard text; the details go to the log.

| Situation | Status |
|---|---|
| Request body over `max_request_body` | `413` |
| Malformed form body | `400` |
| No free worker within `allocate_timeout`, queue full, or kest shutting down | `503` |
| `exec_ttl` or the route `timeout` expired | `504` |
| The worker reported an error (`$worker->error()`) | `500` |
| The worker crashed, broke the protocol, or sent an invalid or too large response | `502` |

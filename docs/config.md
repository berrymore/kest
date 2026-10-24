# Configuration reference

kest reads one YAML file (`kest serve -c kest.yaml`). This page describes every option that exists
today. Check a file without starting anything with `kest validate -c kest.yaml`; add `-dump` to print
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
    type: <cluster type>
    config: {}
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
| `GET /ready` | `200 ready` while serving traffic; `503 starting` / `503 draining` otherwise |
| `GET /server_info` | JSON: version, commit, Go version, state, start time, uptime, config file |

The admin API has no authentication. Keep it on a loopback or private address.

## `server`

| Field | Default | |
|---|---|---|
| `drain_timeout` | `15s` | After SIGINT/SIGTERM, how long in-flight requests may take to finish before their connections are closed. The same budget then applies to stopping clusters. `0s` closes immediately. |

Shutdown order: `/ready` switches to `503 draining`; listeners stop accepting and drain; clusters stop;
the admin server stops last. A second SIGINT/SIGTERM terminates the process immediately.

## `listeners`

At least one listener is required.

| Field | Required | |
|---|---|---|
| `name` | yes | Unique listener name |
| `address` | yes | `host:port` to bind; must be unique (except port `0`) |
| `http` | yes | HTTP is the only protocol so far |

Server limits are fixed for now: 10s to read request headers, 120s idle keep-alive, 1MB of headers.

### `listeners[].http`

| Field | Required | |
|---|---|---|
| `filters` | no | HTTP filters, run in order before the router. Each is `{type, config}`. No built-in filters exist yet. |
| `virtual_hosts` | yes | At least one |

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
| `timeout` | `30s` | Bounds the whole request. The cluster answers `504` when it expires. `0s` disables it. |

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

No cluster types are built in yet. `php_workers` arrives in milestone M2. Until then, any `type` is
rejected with the list of known types.

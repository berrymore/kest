# Plan: M7 — RPC from PHP and plugins

- **Status:** Active. Approved by the user on 2026-10-26, with plugins at the root of the config (`plugins`, not
  `rpc.services`: RPC is one channel to them, and a plugin may later work without it).
- **Milestone:** M7
- **Backlog:** K-900, K-166, K-167, K-168

## Goal

A PHP worker calls kest the way RoadRunner's PHP libraries call RoadRunner: `roadrunner/goridge`'s RPC client,
unchanged, reaches kest over a unix socket, and `roadrunner/kv`'s PSR-16 cache works against kest's `kv` plugin.
Plugins are named components of the config. A plugin is built in, or an external binary that kest starts and talks
to over a small protocol of its own, so plugins can be written without rebuilding kest. RPC routes `<name>.<Method>`
to the plugin named `<name>`.

Scope (the user, 2026-10-26): the channel, RoadRunner compatible; unix sockets by default; plugins in the spirit of
HashiCorp's (built in or external, called by name); `kv` first, to hone the protocol and the mechanics. Not now:
metrics from PHP, the `informer`, `resetter`, `status` and `app` services, jobs and locks (K-901).

Exit criteria:
- PHP integration tests use `roadrunner/kv` 4.5 (`Cache`, PSR-16) against kest: get, set, has, delete, clear, TTL,
  the `*Multiple` forms, and a storage that doesn't exist (`StorageException`).
- The same tests pass with `kv` run as an external plugin through the Go SDK: to PHP, a built-in plugin and an
  external one are the same thing.
- A plugin that dies is restarted; calls fail fast while it is down; plugins stay up until the workers are gone.
- `make check`, `make test-integration` and `make test-matrix` are green; `make test-linux` for the Linux paths.

## Design notes

Facts about RoadRunner: `research/roadrunner.md` § RPC and § KV; about go-plugin and Vault's mounts:
`research/hashicorp-plugins.md` (both checked in source on 2026-10-26).

**D1. The RPC server** (K-900, schema).

```yaml
rpc:
  address: unix:/run/kest/rpc.sock   # optional, see below; or host:port
  socket_mode: "0660"                # unix addresses only, as for listeners
  max_connections: 1024
  max_frame_size: 16MB
  call_timeout: 30s
```

- **On when `rpc` is configured** (`rpc: {}` for the defaults), off otherwise, as in RoadRunner. When it is on,
  `php_workers` adds `RR_RPC` to its workers' environment (`unix:///abs/path` or `tcp://host:port`), last, like
  `RR_MODE`.
- **Address.** `unix:<path>` or `host:port`, as for listeners, with M6's unix socket code (stale files, the mode set
  before the socket can be reached). **Without `address`**, kest makes a private directory (`0700`) in the temporary
  directory at start, puts the socket there and removes both at stop: only kest's own workers find it, through
  `RR_RPC`, and two instances never collide. An explicit address is for PHP processes kest doesn't start (cron,
  console commands). The path can't contain `:`, which PHP's address parser doesn't take.
- **Protocol** (§ RPC). kest checks the frame (two options, the method length within the payload), routes by the
  name before the last dot, and hands the plugin the method, the codec and the body. It never decodes bodies; the
  answer uses the request's codec, an error is an `ERROR` frame with the message. Unknown names get `net/rpc`'s
  messages (`rpc: can't find service …`), since PHP libraries match on them. One call at a time per connection,
  answered in order: PHP never sends the next call before the answer, and a client that does still gets correct
  answers.
- **Bounds.** `max_connections` (PHP's `MultiRPC` opens up to 50 per process; a connection over the limit is closed
  at once and logged); `max_frame_size` both ways (larger closes the connection; RoadRunner has no limit);
  `call_timeout` (the call's context; an expired call answers an error and the connection stays). Idle connections
  stay open: PHP keeps them for the worker's life.
- **Order.** Plugins start first, then the RPC server, then the clusters, since workers call plugins while they
  boot; they stop in reverse after the clusters, since workers call them while they finish their last requests.
  Stop: no new connections, the calls in progress finish within the budget, connections close, then plugins stop.
- **Metrics:** calls by plugin and result, call duration by plugin, open connections. Labels are configured names
  only, never the method a client sent (unbounded).

**D2. Plugins** (K-900, schema).

```yaml
plugins:                 # at the root, next to clusters
  - name: kv             # RPC: PHP calls kv.Set
    type: kv
    config: { … }        # D3
```

- A new registry category, plugins (ADR-0004), configured like clusters: `name`, `type`, `config`. The name follows
  the rules of other names, without dots (RPC splits at the last dot), and is unique.
- Plugins run whether `rpc` is on or not; RPC is the one way to call them today.
- One Go interface for built-in and external plugins. It lives in `internal/plugin` until the SDK (D5) makes it
  public, so a built-in plugin can also be built as an external one:

  ```go
  type Call struct {
  	Method string // after the plugin's name: "Set" for kv.Set
  	Codec  Codec  // the request's codec: Proto, JSON, Raw, Msgpack, Gob
  	Body   []byte
  }
  type Plugin interface {
  	Call(ctx context.Context, c Call) ([]byte, error) // the answer, in c.Codec; an error is sent as its message
  }
  ```

- Optional, as for clusters: `Start`/`Stop`, and `Serving() bool` (a plugin that can't serve fails `/ready`).
  `ErrUnknownMethod` from `Call` becomes `net/rpc`'s message.

**D3. The `kv` plugin** (K-166, schema).

```yaml
plugins:
  - name: kv
    type: kv
    config:
      storages:
        - name: local      # PHP: (new Factory($rpc))->select('local')
          max_size: 64MB   # keys and values; default 64MB
```

- **In memory only.** A shared store for several kest instances (Redis, …) is what external plugins are for.
- **Compatible with `roadrunner/kv`** (§ KV): `Has`, `Set`, `MGet`, `MExpire`, `TTL`, `Delete`, `Clear`; the `kv.v1`
  messages encoded by hand, as `rrhttp` does (no protobuf dependency); RoadRunner's messages where PHP matches on them
  (`no such storage`). PHP's `Factory` takes the RPC client, whose `kv.` prefix is fixed in `Cache`, so the plugin is
  named `kv` for `roadrunner/kv`.
- **Where kest differs from RoadRunner's memory driver, on purpose:** a timeout in the past deletes the item (PSR-16;
  RoadRunner keeps it forever); an expired item is gone at once (checked on read) and swept by one timer per storage
  (RoadRunner: a goroutine per key); `MExpire` only changes the TTL of keys that exist (RoadRunner creates them, or
  replaces values with empty ones); an empty key list answers nothing instead of an error.
- **Bounded.** `max_size` per storage. When a write would go over it, expired items go first, then the least recently
  used. An item larger than `max_size` is an error. Evicting is what a cache does, and this one is empty after a
  restart anyway.
- Storages belong to kest, not to a cluster: every worker of every cluster sees the same data.

**D4. External plugins** (K-168, schema).

```yaml
plugins:
  - name: billing
    type: external
    config:
      command: [/usr/local/bin/kest-billing, --verbose]   # dir: the config file's directory by default
      env: { API_URL: "https://…" }                        # added to kest's environment (inherit_env: true)
      plugin_config: { … }                                 # handed to the plugin as JSON
      max_concurrent_calls: 64
      start_timeout: 10s
      stop_timeout: 10s
```

- **One process per plugin.** It runs in its own process group, with `Pdeathsig` on Linux; its stdout and stderr go
  to the log line by line (`plugin`, `plugin_pid`). The process code of `internal/worker` is reused.
- **Transport: a socketpair.** kest keeps one end; the plugin gets the other as fd 3 (`KEST_PLUGIN_FD=3`,
  `KEST_PLUGIN_PROTOCOL=1`). No path on disk, nobody else can connect, no handshake line on stdout, and a stray print
  to stdout can't break the protocol.
- **Frames: goridge's** (`internal/relay`), the same framing as the workers' and PHP's RPC.
  - Handshake: kest sends `CONTROL` JSON `{"protocol":1,"name":"billing","config":{…}}`; the plugin answers
    `{"protocol":1}`, or an `ERROR` frame with the reason (a bad config fails the start:
    `plugins[1].config.plugin_config: <reason>`). Bounded by `start_timeout`.
  - A call is an RPC request frame with kest's own id in `options[0]`; the answer is an RPC response with the same
    id. Calls from many PHP connections share the plugin's connection and are answered in any order, up to
    `max_concurrent_calls`; a call waits for a free place within its timeout.
  - When a call's context ends, PHP gets an error and the plugin a `CONTROL` frame `{"cancel":<id>}`; the SDK
    cancels the call's context, and a late answer is dropped.
  - Stop: `CONTROL` `{"stop":true}`; the plugin finishes its calls and exits; SIGKILL after `stop_timeout`.
  - Reserved: calls from the plugin to kest (a plugin that hands jobs to a worker pool, as RoadRunner's jobs does).
    The frames leave room for it (ids per direction); M7 has no such calls.
- **Failure.** A plugin that exits or breaks the protocol fails its calls in flight, and is restarted with the
  workers' backoff (100 ms … 30 s). While it is down, calls fail at once (no queue) and `/ready` answers `503`.
- **Trust.** The config is the operator's; kest runs what it names. No checksums: Vault's catalog has them because
  plugins are registered through an API.

**D5. The Go SDK** (K-168): `pkg/plugin`, kest's first public package (rules § 2: needs the ADR). The interface of D2
moves there from `internal/plugin`.

```go
// Serve runs the plugin protocol on the descriptor kest passed, until kest stops the plugin.
func Serve(newPlugin func(config json.RawMessage) (Plugin, error))
```

- Started without kest (no `KEST_PLUGIN_FD`), `Serve` says it is a kest plugin and exits 1.
- Built-in plugins implement the same `Plugin`, so the e2e test builds `kv` as an external plugin with a main of a
  few lines and runs the PHP kv tests against it.
- The protocol is documented (`docs/plugins.md`), so plugins in other languages are possible: PHP's goridge already
  speaks the frames.

**D6. ADR-0010** (K-167): the architecture of D2, D4 and D5: plugins by name; one interface for built-in and
external plugins; a protocol of our own on goridge frames instead of `hashicorp/go-plugin` (gRPC, yamux, hclog: against
ADR-0005, and Go-only in practice); a process per plugin; the reserved direction from plugins to kest; `pkg/plugin`.
Drafted with the plan on 2026-10-26; `Proposed` until the user accepts it, which K-168's code waits for.

## Steps

1. [x] Plan, research (`research/roadrunner.md` § RPC, § KV), backlog — acceptance: the user approves the plan and
   the schema of D1–D4.
2. [ ] K-900 the RPC server and plugins (D1, D2) — acceptance: unit tests on frames (golden frames from goridge's
   codec, fuzz), routing, `net/rpc`'s error messages, the bounds, start and stop order; a PHP integration test calls
   a test plugin through `RPC::create(getenv('RR_RPC'))`; the private default socket; `docs/config.md`.
3. [ ] K-166 `kv` (D3) — acceptance: storage semantics in `synctest` (TTL, sweeping, eviction); protobuf golden bytes
   from PHP's `roadrunner/api-dto`; PHP integration tests with `roadrunner/kv`'s `Cache`; `docs/config.md` and a
   usage section in the docs.
4. [~] K-167 ADR-0010 (D6, drafted), from the research note on HashiCorp's plugins (`research/hashicorp-plugins.md`, done) —
   acceptance: the user accepts or adjusts it.
5. [ ] K-168 external plugins and `pkg/plugin` (D4, D5) — acceptance: the PHP kv tests pass against `kv` as an
   external plugin; tests for a crash and the restart, the start timeout, a bad config, cancellation, stop order and
   stray stdout; `make test-linux`; `docs/plugins.md` (the protocol), `docs/config.md`.
6. [ ] Docs review (`review.md`), `make test-matrix`; the PR on the user's OK — acceptance: the review's findings
   fixed.

## Risks / open questions

- `pkg/plugin` makes an interface and a protocol public before 1.0. The handshake carries the protocol version;
  breaking changes before 1.0 go into the changelog.
- `kest validate` can't check an external plugin's `plugin_config`: only the plugin can, at start. A
  validate-only handshake can come later.
- `kv` in memory belongs to one kest. Several instances (the user's Nomad jobs) don't share it; a shared store is an
  external plugin.
- An explicit `rpc` address is guarded by `socket_mode` (unix) or by binding to loopback (TCP); there is no
  authentication, as in RoadRunner.

## Log

- 2026-10-26 — The user picked M7 and asked what the channel would serve without RR's plugins. Metrics from PHP: not
  now. The user proposed plugins after HashiCorp's (mounted by name, built in or external binaries) and `kv` as the
  first. RoadRunner's RPC and kv checked in source (`research/roadrunner.md` § RPC, § KV). Drafted on
  `feat/m7-rpc`.
- 2026-10-26 — The user approved the plan with one change: plugins at the root of the config (`plugins`), since RPC
  is only the channel from the workers, and a plugin may work with kest without it one day. "Services" became
  "plugins" throughout; the name of a plugin is what RPC routes by.

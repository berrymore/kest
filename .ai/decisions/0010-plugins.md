# ADR-0010: Plugins by name, built in or external over goridge frames, called from PHP through RPC

- **Status:** Proposed
- **Date:** 2026-10-26
- **Deciders:** user, agent

## Context

M7 gives PHP workers a channel to kest that RoadRunner's PHP libraries can use unchanged (`plans/M7-rpc.md`). In
RoadRunner that channel is Go's `net/rpc` with goridge's codec, and every method belongs to a plugin compiled into
the binary: `kv.Set`, `lock.Lock`, `jobs.Push` (`research/roadrunner.md` § RPC). kest has no such plugins, and the
user doesn't want every service to need a rebuild of kest.

The user proposed plugins in the spirit of HashiCorp's: a plugin has a name, and it is either built into kest or an
external binary that kest starts. Plugins are components of their own, configured at the root next to clusters: RPC
is only the channel from the workers to them, and a plugin may one day work with kest without it. What HashiCorp does (`research/hashicorp-plugins.md`):

- **Vault** mounts a plugin type at a path, any number of times; the router strips the prefix and calls one
  interface, `logical.Backend`, which compiled-in and external plugins both implement.
- **go-plugin** runs a plugin as a child process: a handshake line on stdout names a socket, the host connects with
  gRPC (or net/rpc over yamux), and a broker lets the plugin call the host back. Its known problems: anything a
  plugin prints to stdout before the handshake fails the start; plugins outlive a host that dies hard (no process
  group, no `Pdeathsig`); it never restarts a plugin; it brings `grpc`, `protobuf`, `yamux`, `hclog` and
  `protoreflect`.

Constraints in kest: stdlib first (ADR-0005), compiled-in extensions in a registry (ADR-0004), a public `pkg/` API
only with an ADR (rules § 2), bounded resources, and code that already handles goridge frames (`internal/relay`)
and supervised child processes (`internal/worker`).

## Decision

We will:

1. **Configure plugins by name at the root.** `plugins` lists them (`name`, `type`, `config`), like clusters; plugin
   types live in a registry category. The `rpc` block only configures the channel. A call `kv.Set` goes to the plugin
   named `kv` (the name before the last dot, as in `net/rpc`) with the method `Set`; kest routes the method, the codec
   and the raw body, and never decodes bodies.
2. **Use one interface for built-in and external plugins**, `Plugin.Call(ctx, Call) ([]byte, error)`, made public in
   `pkg/plugin`. Built-in plugins (`kv` first) implement it, so any of them can also be built as an external one.
3. **Run external plugins with a protocol of our own on goridge frames**, not go-plugin:
   - one process per plugin (the `external` type), in its own process group, with `Pdeathsig` on Linux, restarted
     with backoff when it dies;
   - a socketpair as the transport, passed as fd 3, so there is no socket file, no handshake on stdout, and the
     plugin's stdout and stderr are just logs;
   - a `CONTROL` handshake with the protocol version, the plugin's name and its config as JSON;
   - calls as RPC frames with kest's ids, many in flight and answered in any order, bounded per plugin; cancel and
     stop as `CONTROL` frames, then SIGKILL after a timeout;
   - room for calls from the plugin to kest (ids per direction), unused until a plugin needs them (one that hands
     jobs to a worker pool).
4. **Publish a small Go SDK**, `pkg/plugin` (`Plugin`, `Call`, `Serve`), and document the protocol
   (`docs/plugins.md`) so plugins can be written in other languages. Before 1.0 the protocol and the package may
   change; the handshake carries the version, and breaking changes go into the changelog.
5. **Trust the config.** kest runs the binaries its config names: no catalog, no checksums, no plugin directory.

## Alternatives considered

- **hashicorp/go-plugin** — mature and known, with gRPC's tooling. Against it: five dependencies (gRPC alone is
  large), the stdout handshake and its failure mode, no supervision, and in practice Go-only plugins. kest needs a
  fraction of it: one connection, request and answer, cancel and stop.
- **gRPC over a unix socket, without go-plugin** — any language gets a generated client. But it needs `grpc` and
  `protobuf` (or M10's choice first), and every plugin method needs a schema, while kest only forwards bytes that
  PHP's libraries already encode.
- **The plugins speak `net/rpc`/goridge on a socket of their own and kest proxies it** — no new protocol, but every
  plugin opens a socket file (paths, modes, stale files) and kest still needs its own handshake and stop.
- **Built-in plugins only** (RoadRunner's model, ADR-0004) — the simplest, but every plugin means a rebuild of
  kest, which is what the user wants to avoid.
- **Go's `plugin` package** — ruled out in `context.md`: fragile, exact toolchain and dependency match.
- **Plugins inside the `rpc` block** (`rpc.services`) — the first draft. But it ties plugins to one channel, while a
  plugin is a component of its own.
- **A catalog of plugin types with multiplexing** (Vault: a binary declares types, one process serves several
  mounts) — more flexible, and later plugins might provide filters or clusters too. Not needed for one process per
  plugin; it can come on top of this protocol.

## Consequences

- Positive: PHP's libraries work unchanged; a plugin can be built in or shipped as a separate binary with the same
  code; no new dependencies; plugins can't break the protocol by printing, and die with kest.
- Negative / costs: a protocol and a public package to keep stable; a supervisor for plugin processes; an external
  plugin's config is only checked when it starts (`kest validate` can't); calls to an external plugin cost a hop
  over a socket.
- Follow-ups: K-168 (external plugins, `pkg/plugin`, `docs/plugins.md`); calls from plugins to kest when a plugin
  needs them; a validate-only handshake if `kest validate` should check plugin configs.

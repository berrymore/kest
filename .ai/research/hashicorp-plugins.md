# HashiCorp plugins — prior art notes

Legend: ✅ verified (with a source link) · ❓ unverified. Checked on 2026-10-26 for M7 (plugins for kest's RPC
services, `plans/M7-rpc.md`), in source at:

- **GP** = [hashicorp/go-plugin `fa60f8a`](https://github.com/hashicorp/go-plugin/tree/fa60f8a91608db94647a9dc739b3a33634d35163)
  (HEAD on 2026-10-12)
- **V** = [hashicorp/vault `1a37452`](https://github.com/hashicorp/vault/tree/1a37452f444e293309eaafc6f934da7a1c79a0cb)
  (HEAD on 2026-10-26)

## go-plugin ✅

- **Launch.** The host sets `<MagicCookieKey>=<value>`, `PLUGIN_MIN_PORT`/`MAX_PORT`, `PLUGIN_PROTOCOL_VERSIONS`,
  and, when configured, `PLUGIN_CLIENT_CERT` (AutoMTLS), `PLUGIN_MULTIPLEX_GRPC`, `PLUGIN_UNIX_SOCKET_*`
  (GP `client.go` L638–L720). The host's environment is inherited unless `SkipHostEnv`. Optional `SecureConfig`
  hashes the binary at its path before exec (a time-of-check/time-of-use gap).
- **The magic cookie is "not a security measure, just a UX feature"**: without it `Serve()` prints that the binary
  is a plugin and exits 1 (GP `server.go` L49–L52, L249–L266; Vault says the same in `sdk/plugin/serve.go`).
- **Handshake: one line on stdout**, `CORE|APP-VERSION|NETWORK|ADDR|PROTOCOL|SERVER-CERT[|MUX]`, e.g.
  `1|3|unix|/path/to/socket|grpc` (GP `server.go` L426–L446, `docs/internals.md`). The plugin listens on a unix
  socket in the temporary directory (TCP on Windows). The host waits for the line within `StartTimeout` (1 min);
  anything printed to stdout before it fails the start ("Unrecognized remote plugin message", issues #199, #183).
- **stdout and stderr afterwards.** The plugin swaps Go's `os.Stdout`/`os.Stderr` for pipes carried to the host over
  the RPC connection (discarded by default); the real fd 1 is drained and dropped, the real fd 2 is logged line by
  line (hclog JSON keeps its level; `panic:` switches to error) (GP `client.go` L811–L818, L1169–L1260).
- **Transports.** One connection per plugin process: net/rpc over yamux (stream 1 control, 2 and 3 stdio), or gRPC
  (health, broker, controller and stdio services always registered). Calls back into the host go through a broker:
  `MuxBroker` hands out yamux streams; `GRPCBroker` opens a new listener per service and the other side dials it.
  Vault's plugins reach their storage this way.
- **Versions.** The core version must match exactly (`1`); the app protocol version is the highest both sides list.
- **Health.** `Client.Ping()` exists; the library never polls by itself.
- **Stop.** `Client.Kill`: a quit/shutdown call (gRPC's is non-graceful, with a TODO), 2 s, then SIGKILL; the
  plugin ignores SIGINT, so Ctrl-C in a shared terminal doesn't kill it.
- **No restart.** A client can't be started again once it was killed (GP `client.go` L578): restarting is the
  application's job. A crash is seen by a goroutine waiting on the process; calls in flight fail.
- **No `Setpgid` or `Pdeathsig`**: plugins outlive a host that dies hard (issue #10, orphans hot-looping).
- **Reattach** connects to a running plugin (host upgrades, debugging, the in-process test mode).
- **Dependencies** (`go.mod`): `go-hclog`, `yamux`, `jhump/protoreflect`, `oklog/run`, `google.golang.org/grpc`,
  `google.golang.org/protobuf`.

## Vault ✅

- **Mounts.** A mount table entry has a `Path` (`secret/`), a `Type` (the plugin name), a UUID for its storage view,
  and the running version and sha256 (V `vault/mount.go` L342–L368). One type can be mounted at many paths, each
  with its own backend instance and storage. The router (a radix tree, no nested mounts) takes the longest prefix,
  strips it from `req.Path`, sets `MountPoint`, attaches the mount's storage, and restores the request afterwards
  (V `vault/router.go` L583–L660).
- **One interface, built in or external.** `logical.Backend` (`HandleRequest`, `Setup`, `Cleanup`, …; V
  `sdk/logical/logical.go` L43–L91) is implemented by compiled-in factories and by external plugins alike; a catalog
  entry's `Builtin` flag picks an in-process call or a go-plugin gRPC client (`sdk/plugin/plugin_v5.go`). Request
  data travels as JSON. The plugin calls Vault back for `Storage` and `SystemView` through the broker.
- **Lifecycle.** At mount the plugin runs briefly in "metadata mode" to report its type; the real process starts
  lazily on the first request. A call that finds the process dead restarts it once and retries once
  (`builtin/plugin/backend.go` L174–L226): restarts are per request, not a supervisor.
- **Catalog.** Plugins are registered through the API by name with `command` + `sha256` inside a configured plugin
  directory (symlinks out of it refused), or by version from a signed artifact. Vault builds the plugin's environment
  itself (`SkipHostEnv`). Mounts pin a version.
- **Multiplexing.** A plugin may serve several mounts in one process (`ServeMultiplex`); the host shares a process
  per name, version, command, args, env and checksum, and tags every call with the mount's `multiplex_id`; the last
  mount gone kills the process.

## What kest takes, and what it does differently

- Takes: mounts by name with one interface for built-in and external services; the "this is a plugin" exit when
  run by hand; a protocol version in the handshake; a stop request, then a kill after a timeout; room for calls from
  the plugin back to the host.
- Differs (`plans/M7-rpc.md` D4, ADR-0010): no stdout handshake and no socket file, a socketpair on fd 3 instead,
  so a stray print can't break anything; process groups and `Pdeathsig`, so plugins don't outlive kest; a supervisor
  that restarts with backoff; goridge frames and no dependencies instead of gRPC and yamux; plugins named in the
  operator's config, so no catalog and no checksums; one process per mount (multiplexing later, if ever).

## ❓

- ❓ Which Vault release brought multiplexing to secrets and auth plugins (database plugins had it first).
- ❓ Terraform's provider debugging through Reattach (`TF_REATTACH_PROVIDERS`).

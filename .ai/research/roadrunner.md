# RoadRunner — prior art notes

Legend: ✅ verified (with a source link) · ❓ unverified (from memory; verify before relying on it).
K-020 verified the frame format and the worker protocol (2026-10-24). K-040 will verify the HTTP payload.

## Versions ✅ (checked 2026-10-24)

RoadRunner moved from calendar versions (last: v2025.1.15, 2026-06-17) to semver: **v3.0.0** (2026-10-09),
current **v3.0.1** (2026-10-21), module `github.com/roadrunner-server/roadrunner/v3`. Its worker stack:

| Go module | Version | Commit | Role |
|---|---|---|---|
| `github.com/roadrunner-server/goridge/v4` | v4.0.0 | [`a83478a`](https://github.com/roadrunner-server/goridge/tree/a83478ade026cbc0d2968333c40f0f895a031ec3) | frame codec, pipe/socket relays |
| `github.com/roadrunner-server/pool/v2` | v2.0.0 | [`c0b00f9`](https://github.com/roadrunner-server/pool/tree/c0b00f97a646bf37ff9772be93ad5dfd6dfd1fe5) | worker process, pool, supervisor |
| `github.com/roadrunner-server/server/v6` | v6.0.0 | [`6e60760`](https://github.com/roadrunner-server/server/tree/6e60760ccc76559aa54c107d57703a23566259f7) | command, env, relay selection |

The wire format is the same in goridge v3.8.3 (used by RR v2025.1.x) and v4.0.0; v4 only changed Go
internals (tiered buffer pools, `writev`). Links below are relative to these commits.

PHP packages were renamed from `spiral/*` to `roadrunner/*` on 2026-10-10 (`roadrunner/worker` 3.8.0). The old
names are still published from the same repositories with the same versions:

| Package (new name) | Old name | Latest | Repo / commit | Notes |
|---|---|---|---|---|
| `roadrunner/worker` | `spiral/roadrunner-worker` | v3.8.2 (2026-10-19) | [roadrunner-php/worker `35fcf09`](https://github.com/roadrunner-php/worker/tree/35fcf09b55de68954e03dc727b710b963db9517d) | PHP ≥ 8.2, ext-json, ext-sockets, psr/log |
| `roadrunner/goridge` | `spiral/goridge` (replaced) | 4.6.0 (2026-10-19) | [roadrunner-php/goridge `887bd3a`](https://github.com/roadrunner-php/goridge/tree/887bd3a6f89a0b95252db608856d33d7a1284406) | `replace: spiral/goridge` |
| `roadrunner/http` | `spiral/roadrunner-http` | v4.2.1 (2026-10-19) | roadrunner-php/http | M2 (K-040) |
| `roadrunner/roadrunner` | — | v3.0.1 | roadrunner-server/roadrunner | **metapackage**, no code: marks the server major (`^3.0`); `Version::current()` reads it |

Source: `https://repo.packagist.org/p2/<vendor>/<package>.json` and `composer require` into a scratch directory.
PHP namespaces stay `Spiral\RoadRunner\…` and `Spiral\Goridge\…`.

## Frame format ✅

Sources: goridge [`pkg/frame/doc.go`](https://github.com/roadrunner-server/goridge/blob/a83478ade026cbc0d2968333c40f0f895a031ec3/pkg/frame/doc.go),
[`frame.go`](https://github.com/roadrunner-server/goridge/blob/a83478ade026cbc0d2968333c40f0f895a031ec3/pkg/frame/frame.go),
[`frame_flags.go`](https://github.com/roadrunner-server/goridge/blob/a83478ade026cbc0d2968333c40f0f895a031ec3/pkg/frame/frame_flags.go),
[`internal/receive.go`](https://github.com/roadrunner-server/goridge/blob/a83478ade026cbc0d2968333c40f0f895a031ec3/internal/receive.go);
PHP [`src/Frame.php`](https://github.com/roadrunner-php/goridge/blob/887bd3a6f89a0b95252db608856d33d7a1284406/src/Frame.php),
[`src/StreamRelay.php`](https://github.com/roadrunner-php/goridge/blob/887bd3a6f89a0b95252db608856d33d7a1284406/src/StreamRelay.php).

| Bytes | Field |
|---|---|
| 0 | version (upper 4 bits, always `1`) · header length HL in 32-bit words (lower 4 bits, `3` without options) |
| 1 | flags (below) |
| 2–5 | payload length, uint32 little-endian (so up to 4 GiB) |
| 6–9 | CRC32 (IEEE) of bytes **0–5 only**, little-endian |
| 10 | stream bits: `STREAM 0x01`, `STOP 0x02`, `PING 0x04`, `PONG 0x08` |
| 11 | reserved (0) |
| 12… | options: `HL − 3` uint32 LE words, at most 10 (40 bytes) |
| then | payload |

Flags (byte 1, bit OR): `CONTROL 0x01`, `CODEC_RAW 0x04`, `CODEC_JSON 0x08`, `CODEC_MSGPACK 0x10`
(deprecated: RR v3 drops the connection on it), `CODEC_GOB 0x20`, `ERROR 0x40`, `CODEC_PROTO 0x80`.

Facts that matter for an implementation:
- The CRC does not cover byte 10, the options or the payload.
- Nobody checks the version on receive. PHP doesn't verify the CRC either; Go does, and on a mismatch reads
  the rest of the stream (2 s deadline when the reader supports one) to put the raw bytes in the error.
- No payload limit on either side. Go allocates directly above 10 MB. **kest must bound frame size itself.**
- PHP packs lengths, CRC and options with `pack('L')` (machine byte order), which is LE on amd64/arm64.

## Worker protocol ✅

Sources: pool [`internal/protocol.go`](https://github.com/roadrunner-server/pool/blob/c0b00f97a646bf37ff9772be93ad5dfd6dfd1fe5/internal/protocol.go),
[`worker/worker.go`](https://github.com/roadrunner-server/pool/blob/c0b00f97a646bf37ff9772be93ad5dfd6dfd1fe5/worker/worker.go),
[`ipc/pipe/pipe.go`](https://github.com/roadrunner-server/pool/blob/c0b00f97a646bf37ff9772be93ad5dfd6dfd1fe5/ipc/pipe/pipe.go);
PHP [`src/Worker.php`](https://github.com/roadrunner-php/worker/blob/35fcf09b55de68954e03dc727b710b963db9517d/src/Worker.php),
[`src/PayloadFactory.php`](https://github.com/roadrunner-php/worker/blob/35fcf09b55de68954e03dc727b710b963db9517d/src/PayloadFactory.php).
**Also checked by experiment** on 2026-10-24: a Go program spoke this protocol to a real `roadrunner/worker`
3.8.2 echo worker on PHP 8.5.12 over pipes; every frame below matched.

| Message | Direction | Flags | Options | Payload |
|---|---|---|---|---|
| Handshake request | server → worker | `CONTROL\|CODEC_JSON` | none | `{"pid":<server pid>}` |
| Handshake reply | worker → server | `CONTROL` (no codec flag) | none | `{"pid":<getmypid()>}` |
| Request | server → worker | the payload codec (RR: `Payload.Codec`) | `[len(context)]` | context ‖ body |
| Response | worker → server | codec if the app passed one, else `0` | `[len(header)]` | header ‖ body |
| Soft error | worker → server | `ERROR` | none | error message |
| Worker asks to be replaced | worker → server | `0` | `[13]` | header `{"stop":true}`, empty body |
| Stop | server → worker | `CONTROL\|CODEC_JSON` | none | `{"stop":true}` |

- **Handshake.** The server sends its own pid; PHP requires a non-empty `pid` (`!empty($command['pid'])`), so it
  must not be 0. PHP answers from inside `waitPayload()`, i.e. only after the application has booted, so the
  handshake timeout also bounds application boot. RR checks only: `CONTROL` flag set, valid JSON, pid > 0. It
  does **not** compare the reported pid with the child pid; signals and RSS use the child pid.
- **Request/response.** Exactly one option, the context/header length. PHP indexes `options[0]` without a check,
  so a data frame without options breaks the PHP side; Go rejects a response without exactly one option
  (`Decode` error) or with an offset past the payload.
- **Soft error** (`$worker->error()`): RR returns the worker to the pool and the error to the caller.
- **Worker-initiated stop** (`$worker->stop()`, sent instead of a response): RR marks the worker invalid, stops
  it, and re-runs the same request on another worker (`goto begin`, no retry limit).
  `roadrunner/http`'s `PSR7Worker` never calls it; it is for applications.
- **Stop.** PHP's `waitPayload()` returns `null`, the app loop ends, the process exits (status 0 observed). RR's
  `Process.Stop` waits a hard-coded 10 s for the exit, then sends SIGKILL.
- **Crash mid-request.** The read fails (EOF), RR classifies it as a network error, kills the worker and fails the
  request. No retry.
- **Streaming** (byte 10): a response with `STREAM` set means more frames follow; the server cancels with a
  `STOP`-bit frame; during a stream PHP sets `PING` every 5 frames and the server answers `PONG`. Not needed
  before streaming responses (Q-C).

## Process management in RR ✅

Sources: server [`plugin.go`](https://github.com/roadrunner-server/server/blob/6e60760ccc76559aa54c107d57703a23566259f7/plugin.go),
[`api.go`](https://github.com/roadrunner-server/server/blob/6e60760ccc76559aa54c107d57703a23566259f7/api.go);
pool [`process/isolate.go`](https://github.com/roadrunner-server/pool/blob/c0b00f97a646bf37ff9772be93ad5dfd6dfd1fe5/process/isolate.go);
PHP [`src/Environment.php`](https://github.com/roadrunner-php/worker/blob/35fcf09b55de68954e03dc727b710b963db9517d/src/Environment.php),
[`src/Internal/StdoutHandler.php`](https://github.com/roadrunner-php/worker/blob/35fcf09b55de68954e03dc727b710b963db9517d/src/Internal/StdoutHandler.php).

- **Environment.** RR passes its whole environment, plus `RR_RELAY` (`pipes`, `tcp://…`, `unix://…`), `RR_RPC`
  (when the rpc plugin is on), `RR_VERSION` and `server.env` (keys uppercased). The http plugin adds `RR_MODE=http`.
  PHP reads `RR_MODE`, `RR_RELAY` (default `pipes`), `RR_RPC` (default `tcp://127.0.0.1:6001`) and `RR_VERSION`
  from `$_ENV` + `$_SERVER`.
- **Process group.** Workers get their own group (`Setpgid`) so terminal signals don't reach them. Kill is
  SIGKILL to the worker pid only (grandchildren survive). No `Pdeathsig`.
- **Stdout/stderr.** Stdout is the relay. `Worker::create()` installs an output-buffer handler that sends
  `echo`/`print` output and `header()` warnings to stderr. Anything printed before that (bootstrap errors:
  PHP CLI defaults to `display_errors=STDOUT`) corrupts the relay and surfaces as a CRC error with the raw bytes.
  RR logs each stderr chunk at info level. RR special-cases stdout starting with `Could not op` ("Could not open
  input file"), but PHP 8.5.12 prints that message to **stderr** (checked locally).

## Pool and supervisor in RR ✅

Sources: pool [`pool/config.go`](https://github.com/roadrunner-server/pool/blob/c0b00f97a646bf37ff9772be93ad5dfd6dfd1fe5/pool/config.go),
[`pool/static_pool/pool.go`](https://github.com/roadrunner-server/pool/blob/c0b00f97a646bf37ff9772be93ad5dfd6dfd1fe5/pool/static_pool/pool.go),
[`pool/static_pool/supervisor.go`](https://github.com/roadrunner-server/pool/blob/c0b00f97a646bf37ff9772be93ad5dfd6dfd1fe5/pool/static_pool/supervisor.go),
[`worker_watcher/worker_watcher.go`](https://github.com/roadrunner-server/pool/blob/c0b00f97a646bf37ff9772be93ad5dfd6dfd1fe5/worker_watcher/worker_watcher.go),
[`worker/options.go`](https://github.com/roadrunner-server/pool/blob/c0b00f97a646bf37ff9772be93ad5dfd6dfd1fe5/worker/options.go);
gopsutil v3.21.11 `process/process_{linux,darwin}.go`.

- **Knobs and defaults:** `num_workers` (NumCPU, hard cap 500), `max_jobs` (0 = unlimited), `max_queue_size`
  (0 = **unbounded**), `allocate_timeout` 60s, `destroy_timeout` 60s, `reset_timeout` 60s, `stream_timeout` 60s;
  `supervisor`: `watch_tick` 5s, `ttl`, `idle_ttl`, `exec_ttl` (0 = **no limit**), `max_worker_memory` (integer MB).
- **max_jobs jitter:** each worker's limit gets +0…14 % (crypto/rand), so workers don't all restart together.
- **Supervisor**, every `watch_tick`: `ttl` counts from creation; `max_worker_memory` compares RSS; `idle_ttl`
  counts from the last use, and a worker that was never used is never idle-reaped. A ready worker over a limit is
  stopped; a busy one is marked invalid and stopped when released.
- **exec_ttl** is a per-exec context timeout: on expiry the worker is killed and the request fails. Without it
  RR uses `context.Background()`: a client disconnect doesn't abort the exec, and a hung worker hangs forever.
- **RSS:** Linux reads `/proc/<pid>/statm`; macOS runs `ps -o rss,vsize,pagein -p <pid>` per worker per tick.
- **Startup:** workers are spawned in parallel; any failure fails pool creation. Spawn + handshake is bounded
  by `allocate_timeout`.
- **Respawn:** a replacement is spawned when a worker exits. On failure RR retries every 1 s until
  `allocate_timeout`, then drops the slot; when no workers remain it **panics** ("no workers available").
  No backoff.
- **Error classes:** soft error → worker back to the pool; network/CRC/decode error → kill; exec_ttl → kill;
  worker stop request → replace and retry the request.

## HTTP over the worker protocol ❓ (K-040)

- Request context: method, URI, protocol, headers, cookies, remote address, uploads, attributes, raw query;
  the body is sent separately. Older versions used JSON; `roadrunner/http` requires `roadrunner/api-dto`, so
  newer ones likely use protobuf (`http.v1`?).
- Response: the context carries status and headers; the body is the response body.
- Streaming responses (the `STREAM` bit above) and `103 Early Hints` may exist in newer versions.
- Uploads: RR writes uploaded files to temp files and passes an upload tree; the PHP library maps it to PSR-7.
- The http plugin sets `RR_MODE=http` ✅ (http plugin `RrMode`); frameworks may branch on it ❓.

## Other RR notes ❓

- Plugins are wired by the Endure container; the RPC plugin (goridge RPC over TCP, default `127.0.0.1:6001`)
  is used by the `rr` CLI (`reset`, `workers`) and by some PHP libraries.
- RR server features kest doesn't plan yet: `server.user`/`group`, `server.on_init`, `pool.debug`,
  `pool.dynamic_allocator` ✅ (seen in the sources above).

## Questions

K-020 (answered above, 2026-10-24):
1. ✅ Frame layout, flags, CRC coverage, options → "Frame format".
2. ✅ Handshake and pid → "Worker protocol".
3. ✅ Stop sequence, kill mid-request → "Worker protocol", "Pool and supervisor".
4. ✅ Soft error vs crash → "Worker protocol", "Pool and supervisor".

K-040:
5. HTTP request context codec in current `roadrunner/http`; is JSON still accepted?
6. Upload tree format; who cleans up temp files.
7. Streaming response protocol; early hints.
8. Which PHP libraries require the RPC plugin (metrics, KV, jobs, `rr reset` from PHP…)?

## What to learn from RR / what we do differently

- Learn: supervisor limits, max_jobs jitter, worker lifecycle edge cases, the PSR-7 integration, the
  framework adapters.
- Differ: an Envoy-style routing graph with several pools; an explicit registry instead of DI; strict
  config validation with path-aware errors; WASM as a first-class cluster type; bounded defaults where RR has
  none (queue, exec time, frame size); respawn with backoff instead of a panic.

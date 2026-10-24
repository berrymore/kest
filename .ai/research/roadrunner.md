# RoadRunner — prior art notes

Legend: ✅ verified (with a source link) · ❓ unverified (from memory; verify before relying on it).
K-020 and K-040 turn the ❓ items into ✅ with links.

## PHP packages ✅ (Packagist metadata, checked 2026-10-24)

Source: `https://repo.packagist.org/p2/<vendor>/<package>.json`

| Package | Latest | PHP | Other requirements |
|---|---|---|---|
| `spiral/roadrunner-worker` | v3.8.2 (2026-10-19) | `>=8.2` | ext-json, ext-sockets, psr/log, roadrunner/goridge, roadrunner/roadrunner |
| `spiral/roadrunner-http` | v4.2.1 (2026-10-19) | `>=8.2` | ext-json, psr/http-factory, psr/http-message, roadrunner/api-dto, roadrunner/worker, symfony/polyfill-php83 |
| `spiral/goridge` | 4.3.0 (2026-10-09) | `>=8.1` | ext-json, ext-sockets, spiral/roadrunner |

Notes:
- `roadrunner/*` requirements look like virtual/alias package names; check in K-020 what provides them ❓.
- `roadrunner/api-dto` in `roadrunner-http` suggests the HTTP request context uses protobuf DTOs ❓ (K-040).
- Local brew PHP 8.5.12 has the required extensions (`sockets`, `json`).

## Sources to verify against

- Server: https://github.com/roadrunner-server (org; main repo `roadrunner`, pool in `pool`, HTTP in `http`) — Go, MIT
- Protocol: https://github.com/roadrunner-server/goridge (Go side), https://github.com/spiral/goridge (PHP side) ❓ exact repo names
- PHP worker libraries: `spiral/roadrunner-worker`, `spiral/roadrunner-http` (Packagist)
- Docs: https://docs.roadrunner.dev

## Process model ❓

- RR spawns PHP processes from `server.command`; each worker runs a loop: wait for a payload → handle → respond.
- Default relay: pipes (worker stdin/stdout). Alternatives: TCP, unix sockets. Worker stderr goes to RR logs
  (which is why PHP workers must not `echo` to stdout).
- One request at a time per worker; concurrency = pool size.
- Plugins are wired by the Endure container; the RPC plugin (goridge RPC over TCP, default `127.0.0.1:6001`)
  is used by the `rr` CLI (`reset`, `workers`) and by some PHP libraries.

## Config knobs worth mirroring ❓

`http.pool.*`: `num_workers`, `max_jobs`, `allocate_timeout`, `destroy_timeout`, and
`supervisor.{watch_tick, ttl, idle_ttl, max_worker_memory, exec_ttl}`.
kest uses similar names under `clusters[].config.pool` / `supervisor` (architecture.md §3).

## Protocol (goridge) ❓

- Messages are framed: a fixed-size header (version, header length, flags, payload length, CRC of the
  header) plus optional options, followed by the payload.
- The payload is split into **context** (metadata) and **body**; flags select the codec of the context
  (raw / JSON / protobuf / msgpack…).
- Control messages: on startup the worker reports its PID (handshake); a stop message asks it to exit;
  a worker error is signaled with an error flag and the message in the body.

## HTTP over the worker protocol ❓

- Request context: method, URI, protocol, headers, cookies, remote address, uploads, attributes, raw query;
  the body is sent separately. Older versions used JSON; newer ones may use protobuf (`http.v1`?).
- Response: the context carries status and headers; the body is the response body.
- Streaming responses (chunked frames with a "stream" flag) and `103 Early Hints` may exist in newer versions.
- Uploads: RR writes uploaded files to temp files and passes an upload tree; the PHP library maps it to PSR-7.

## Questions to answer

K-020:
1. Exact frame layout (byte offsets, byte order), the flags list, CRC algorithm and coverage, options semantics.
2. Handshake sequence at worker start, and how the PID is reported and checked.
3. Stop sequence and timeouts; what happens when the worker is killed mid-request.
4. How a worker error is reported, and how RR distinguishes a soft error (respond with 500) from a crash.

K-040:
5. HTTP request context codec in current `roadrunner-http`; is JSON still accepted?
6. Upload tree format; who cleans up temp files.
7. Streaming response protocol; early hints.
8. Which PHP libraries require the RPC plugin (metrics, KV, jobs, `rr reset` from PHP…)?

## What to learn from RR / what we do differently

- Learn: supervisor limits, worker lifecycle edge cases, the PSR-7 integration, the framework adapters.
- Differ: an Envoy-style routing graph with several pools; an explicit registry instead of DI; strict
  config validation with path-aware errors; WASM as a first-class cluster type.

# RoadRunner — prior art notes

Legend: ✅ verified (with a source link) · ❓ unverified (from memory; verify before relying on it).
K-020 verified the frame format and the worker protocol (2026-10-24); K-040 verified the HTTP payload (2026-10-25).

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
| `roadrunner/http` | `spiral/roadrunner-http` | v4.2.1 (2026-10-19) | [roadrunner-php/http `8aef719`](https://github.com/roadrunner-php/http/tree/8aef71907057d076f132484b32fe6178c370a73d) | `HttpWorker`, `PSR7Worker`; needs `roadrunner/api-dto` (protobuf DTOs) |
| `roadrunner/api-dto` | `roadrunner-php/roadrunner-api-dto` | v2.1.0 | [roadrunner-php/roadrunner-api-dto `90e5b93`](https://github.com/roadrunner-php/roadrunner-api-dto/tree/90e5b93788820a08184ed9c0b821df8272cc24e5) | generated PHP classes; pulls in `google/protobuf` (pure PHP) |
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

## HTTP over the worker protocol ✅ (K-040, checked 2026-10-25)

Sources: Go `github.com/roadrunner-server/http/v6` v6.0.0 ([`e4670d9`](https://github.com/roadrunner-server/http/tree/e4670d9e1ee0e7258d5abe8c70ad2254586c6329)):
[`handler/handler.go`](https://github.com/roadrunner-server/http/blob/e4670d9e1ee0e7258d5abe8c70ad2254586c6329/handler/handler.go),
[`handler/request.go`](https://github.com/roadrunner-server/http/blob/e4670d9e1ee0e7258d5abe8c70ad2254586c6329/handler/request.go),
[`handler/response.go`](https://github.com/roadrunner-server/http/blob/e4670d9e1ee0e7258d5abe8c70ad2254586c6329/handler/response.go),
[`handler/parse.go`](https://github.com/roadrunner-server/http/blob/e4670d9e1ee0e7258d5abe8c70ad2254586c6329/handler/parse.go),
[`handler/uploads.go`](https://github.com/roadrunner-server/http/blob/e4670d9e1ee0e7258d5abe8c70ad2254586c6329/handler/uploads.go),
[`config/config.go`](https://github.com/roadrunner-server/http/blob/e4670d9e1ee0e7258d5abe8c70ad2254586c6329/config/config.go);
messages in `github.com/roadrunner-server/api-go/v6` v6.0.0 ([`32b5879`](https://github.com/roadrunner-server/api-go/tree/32b5879680bf26d199c9f3b9faf58e6374a36897)) `http/v1/http.pb.go`;
PHP [`src/HttpWorker.php`](https://github.com/roadrunner-php/http/blob/8aef71907057d076f132484b32fe6178c370a73d/src/HttpWorker.php),
[`src/PSR7Worker.php`](https://github.com/roadrunner-php/http/blob/8aef71907057d076f132484b32fe6178c370a73d/src/PSR7Worker.php),
[`src/GlobalState.php`](https://github.com/roadrunner-php/http/blob/8aef71907057d076f132484b32fe6178c370a73d/src/GlobalState.php).
**Checked by experiment** on 2026-10-25: a scratch Go client sent both codecs to an unmodified `PSR7Worker`
(`roadrunner/http` 4.2.1, `nyholm/psr7`, PHP 8.5.12) and decoded the answers.

**Codec.** RR v3 sends the request context as protobuf (`CODEC_PROTO`) and rejects JSON responses ("JSON codec is
not supported"). The PHP side detects the codec once per process, on the first request:
`json_validate($payload->header) ? CODEC_JSON : CODEC_PROTO`, and answers in the same codec (frame flag
`CODEC_JSON` or `CODEC_PROTO`). So the current PHP library accepts both. Protobuf support arrived in
`spiral/roadrunner-http` **3.5.0** (2024-04-01, with `roadrunner-api-dto`); 2.x–3.4 speak JSON only. Measured
PHP-side cost per request (10 headers, `PSR7Worker` + `nyholm/psr7`, no `ext-protobuf`, pure-PHP `google/protobuf`
5.36.2): **141 µs with protobuf, 28.5 µs with JSON**. `roadrunner/worker` documents the codec argument of
`respond()` as "will be removed in v4.0 and `CODEC_PROTO` will be used by default".

**Request context, protobuf** (`http.v1.Request`; proto3, empty fields omitted):

| # | Field | Type | RR value |
|---|---|---|---|
| 1 | `remote_addr` | string | IP of `r.RemoteAddr` without the port |
| 2 | `protocol` | string | `r.Proto`: `HTTP/1.1`, `HTTP/2.0` |
| 3 | `method` | string | |
| 4 | `uri` | string | `r.URL.String()` with CR/LF removed; if it has no host: `http://` or `https://` (TLS) + `r.Host` + it |
| 5 | `header` | map<string, HeaderValue> | `r.Header` as is (canonical keys, no `Host`: Go keeps it in `r.Host`) |
| 6 | `cookies` | map<string, HeaderValue> | one value per name, `url.QueryUnescape`d; a later cookie with the same name wins; undecodable ones are dropped |
| 7 | `raw_query` | string | `r.URL.RawQuery` with CR/LF removed |
| 8 | `parsed` | bool | the body is a JSON form tree (below) |
| 9 | `uploads` | bytes | JSON upload tree, only for parsed multipart requests |
| 10 | `attributes` | map<string, HeaderValue> | values set by RR middleware through `attributes.Set`; PHP reads `ipAddress` as the client IP if present |

`HeaderValue` is `{1: repeated bytes value}`. `http.v1.Response` is `{1: int64 status, 2: map<string, HeaderValue>
headers}`. PHP reads `cookies` as `implode(',', values)` and `attributes` as their first value.

**Request context, JSON** (the format before 3.5, still accepted): `{"remoteAddr", "protocol", "method", "uri",
"headers": {name: [values]}, "cookies": {name: value}, "rawQuery", "parsed", "uploads": tree|null, "attributes":
{}}`. The response context is `{"status": int, "headers": {name: [values]}}` (`{}` when there are none). In JSON mode
PHP's `getParsedBody()` runs `json_decode($body, …, JSON_THROW_ON_ERROR)`, so a parsed request must carry a non-empty
body (`{}`); in protobuf mode PHP substitutes `{}` for an empty parsed body itself.

**Body** (`request()` in `request.go`):
- `HEAD` and `OPTIONS`: not read.
- `Content-Type` contains `application/x-www-form-urlencoded`: `r.ParseForm()` (Go reads the body only for POST, PUT
  and PATCH, so a DELETE form body is lost), the `PostForm` values become a JSON tree, `parsed=true`.
- contains `multipart/form-data`: `r.ParseMultipartForm(32 MiB)`; values become the JSON tree, files the upload
  tree, `parsed=true`.
- anything else: the raw body, `parsed=false`. Option `http.raw_body: true` sends form and multipart bodies raw too.
- Errors forming the request: 413 for `http.MaxBytesError`, else 400.
- `http.max_request_size` is in **MB**, default 1000 (`MaxBytesReader`).

**Form tree** (`parse.go`): keys use PHP bracket syntax, `a[b][]` → `["a", "b", ""]` (spaces dropped); depth over 127
is ignored; a scalar key takes the **last** value, `a[]` takes all values; an empty value never overwrites a branch;
a non-empty scalar and a branch under the same key is an error (`invalid multiple values to key`, → 400). RR walks
Go maps, so the order of conflicting keys is random; JSON objects come out with sorted keys.

**Uploads** (`uploads.go`): each file is copied to `os.CreateTemp(uploads.dir, "upload")` (default `os.TempDir()`);
the tree mirrors the form keys and its leaves are `{"name", "mime", "size", "error", "tmpName"}` with PHP's
`UPLOAD_ERR_*` codes: 0 ok, 4 no file (open failed), 6 no temp dir, 7 can't write, 8 extension forbidden
(`uploads.forbid` / `uploads.allow`, lower-cased `path.Ext`, no default list). RR deletes the temp files and the
multipart form's own temp files after the response. `PSR7Worker` turns each leaf into an `UploadedFile` streamed from
`tmpName`.

**Response** (`response.go`): status must be 100–599 (else 500); headers are added value by value; `Trailer` names
are moved to `Trailer:<name>` (Go's trailer prefix); `Http2-Push` triggers server push; 1xx except 101 is written
as an informational response (stream mode only makes sense); 101 is ignored. Every pool error answers
`http.internal_error_code` (500), with a `No-Workers: true` header when no worker was free. An empty response
context means "body only".

**Streaming** (`STREAM` bit): `HttpWorker::respond()` streams when the body is a `Generator`; `PSR7Worker` does so
only when `$chunkSize > 0` (default 0, off). Every chunk repeats status and headers; RR writes them once.
Checked on 2026-10-25 in `roadrunner/worker` 3.8.2 (`Worker.php`, `PayloadFactory.php`), `roadrunner/http` 4.2.1
(`HttpWorker::respondStream`) and Go `github.com/roadrunner-server/pool/v2` v2.0.0-beta.1 (`worker/worker.go`,
`pool/static_pool/pool.go`) ✅:
- Byte 10 of a frame: `0x01` STREAM (more frames follow), `0x02` STOP, `0x04` PING, `0x08` PONG. The last frame of a
  stream has no STREAM bit. `respond(103, '', $headers, false)` sends an informational frame first; RR writes 1xx
  other than 101 as informational responses (`writeInformational`).
- In stream mode the worker sets PING on every fifth frame and then polls stdin without blocking (`hasFrame`) until
  it reads the PONG or a STOP. RR answers every PING with a PONG frame (`sendPONG`: header only, PONG bit).
- When the client goes away, RR sends a frame with only the STOP bit (`StreamCancel`) and reads and drops frames until
  one without STREAM. PHP throws `StreamStoppedException` into the generator and sends an empty final frame. PONG and
  STOP frames that arrive later are skipped by `waitPayload()` (`SkipMessage`).
- RR bounds each frame of a stream by `exec_ttl` (a context per `StreamIterWithContext`), not the whole stream, and
  the drain after STOP by `pool.stream_timeout` (default 1 minute).

**PHP side** (`PSR7Worker`, `GlobalState`): `$_SERVER` gets `REQUEST_URI` = the full `uri` (scheme and host
included), `REMOTE_ADDR` (`attributes.ipAddress` if set), `REQUEST_METHOD`, `HTTP_HOST` from the URI, `HTTP_*` and
`CONTENT_TYPE`/`CONTENT_LENGTH` from the headers.

**Client IP behind proxies** (`proxy_ip_parser` v6.0.0, `plugin.go`, checked 2026-10-25 ✅): enabled by
`http.trusted_subnets` (CIDRs, no defaults in the plugin). If the peer is in a trusted subnet, the first header of
`Forwarded` (`for=`), `X-Forwarded-For` (the **leftmost** address), `X-Real-Ip`, `True-Client-Ip`,
`Cf-Connecting-Ip` (or the `http.trusted_headers` list, in order) that yields a value replaces `r.RemoteAddr`, which
becomes `remote_addr` of the request context. The value isn't validated as an IP, and the leftmost
`X-Forwarded-For` entry is whatever the client sent through a proxy that appends. No `ipAddress` attribute is set. PSR-7 `Host` comes from the URI. The protocol version is the part
after `HTTP/` (`2.0` → `2`). A request with an empty context and an empty body is read as "stop".

**Environment:** the http plugin adds `RR_MODE=http` ✅. PHP reads `RR_VERSION` only through
`Environment::getVersion()`; nothing in `roadrunner/http` or `roadrunner/worker` uses it ✅. Frameworks branch on
`RR_MODE` (e.g. Spiral's dispatchers) ❓.

## Framework runtimes ✅ (checked 2026-10-25)

**Symfony** (`symfony/runtime` v8.1.9 at
[8670c7b](https://github.com/symfony/runtime/tree/8670c7b20dacb5b1bcc07891dc35546376685af1), with
`runtime/roadrunner-symfony-nyholm` 1.0.0; read in the installed sources): the generated
`vendor/autoload_runtime.php`
([template](https://github.com/symfony/runtime/blob/8670c7b20dacb5b1bcc07891dc35546376685af1/Internal/autoload_runtime.template))
picks the runtime class with `$_SERVER['APP_RUNTIME'] ??= $_ENV['APP_RUNTIME'] ?? <class>`, where `<class>` is
`extra.runtime.class` from `composer.json`, by default `SymfonyRuntime`
([ComposerPlugin.php](https://github.com/symfony/runtime/blob/8670c7b20dacb5b1bcc07891dc35546376685af1/Internal/ComposerPlugin.php)).
`.env` is loaded later, by the runtime's constructor
([SymfonyRuntime.php](https://github.com/symfony/runtime/blob/8670c7b20dacb5b1bcc07891dc35546376685af1/SymfonyRuntime.php)),
so `APP_RUNTIME` in `.env` has no effect, although the README of `runtime/roadrunner-symfony-nyholm` shows it
there. It has to be a real environment variable (kest: the cluster's `env`; RoadRunner: `server.env`) or
`extra.runtime.class`. Without it the app handles one request built from the CLI environment (`GET http://:/`),
prints the response to stdout and exits; reproduced with Symfony 8.1 in `playground/` (K-071).

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

K-040 (answered above, 2026-10-25):
5. ✅ Protobuf in RR v3; the PHP library still accepts JSON and answers in the codec it received.
6. ✅ JSON tree in the `uploads` field; RR deletes the temp files after the response.
7. ✅ `STREAM` frames, opt-in on the PHP side (`$chunkSize`, generators); 1xx (early hints) only in stream mode.
8. ✅ for HTTP: `roadrunner/http` and `roadrunner/worker` never call RPC (only `WorkerPool`, which manages pools
   over RPC, does). Libraries for RR plugins (KV, jobs, metrics, lock) need RPC by design ❓ (not checked one by
   one); `PSR7Worker` apps work without it.

## What to learn from RR / what we do differently

- Learn: supervisor limits, max_jobs jitter, worker lifecycle edge cases, the PSR-7 integration, the
  framework adapters.
- Differ: an Envoy-style routing graph with several pools; an explicit registry instead of DI; strict
  config validation with path-aware errors; WASM as a first-class cluster type; bounded defaults where RR has
  none (queue, exec time, frame size); respawn with backoff instead of a panic.

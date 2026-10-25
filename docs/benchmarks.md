# Benchmarks

kest against RoadRunner, serving the same PHP worker on the same machine, with each of kest's codecs and with and
without PHP's `protobuf` extension. Run it yourself with `RR=/path/to/rr make bench` (after `make php-deps`); the
configs, the load generator and the codec benchmark are in [`test/bench`](../test/bench).

## Method

- **Worker:** [`examples/php-hello`](../examples/php-hello), an unmodified `roadrunner/http` PSR-7 loop, 4 workers,
  no recycling (`max_jobs` 0), no filters or middleware.
- **Targets:** kest with the `json` codec (its default), kest with the `proto` codec (what RoadRunner sends), and
  RoadRunner 3.0.1 (the release binary, `server.relay: pipes`). Each target runs alone.
- **PHP:** 8.5.12 (NTS) with `ext-protobuf` 5.36.2 from PECL, the version of the worker's `google/protobuf`
  package; OPcache off (the CLI default), so no JIT. The control run is without the extension: `PHPRC` names a copy
  of `php.ini` without its `extension=protobuf.so` line, and both servers pass their environment to the workers.
- **Scenarios:** `hello` is `GET /?name=bench` (a 41-byte text response); `form` is a `POST /info` with a small
  form that PHP parses and echoes as JSON; `body-1MB` is a `POST /info` with a 1 MB raw body.
- **Load:** [`loadgen`](../test/bench/loadgen), 16 or 64 keep-alive clients, 3 s warm-up, then 15 s measured. Each
  scenario runs 3 times; the tables show the run with the median throughput. Latencies are per request, measured
  by the client.
- **Machine:** an Apple M3 Pro laptop (12 cores, 18 GB), macOS 26.0.1, Go 1.27.3; kest at `c596834` (M4). The load
  generator runs on the same machine and takes part of the CPU, as it would for any of the targets; macOS
  background services took up to one and a half cores at times, and repeated runs differ by up to 7 %. Run on
  2026-10-25.

## Results

Requests per second by codec, with and without `ext-protobuf`:

| Scenario | Clients | kest `json` | kest `proto` | RoadRunner | kest `json`, no ext | kest `proto`, no ext | RoadRunner, no ext |
|---|---|---|---|---|---|---|---|
| hello | 16 | 46 794 | 46 215 | 47 520 | 44 591 | 33 783 | 32 997 |
| hello | 64 | 48 534 | 46 807 | 46 337 | 49 750 | 31 174 | 30 508 |
| form | 16 | 38 458 | 37 587 | 42 911 | 38 245 | 26 393 | 27 972 |
| form | 64 | 42 587 | 41 242 | 41 255 | 43 088 | 27 806 | 25 734 |
| body-1MB | 16 | 2 592 | 2 504 | 2 415 | 2 256 | 2 417 | 2 370 |
| body-1MB | 64 | 2 683 | 2 535 | 2 536 | 2 634 | 2 567 | 2 486 |

Latencies with `ext-protobuf`:

| Target | Scenario | Clients | Requests/s | p50 | p99 |
|---|---|---|---|---|---|
| kest (`json`) | hello | 16 | 46 794 | 317 µs | 717 µs |
| kest (`proto`) | hello | 16 | 46 215 | 322 µs | 717 µs |
| RoadRunner | hello | 16 | 47 520 | 319 µs | 649 µs |
| kest (`json`) | hello | 64 | 48 534 | 1 260 µs | 2 454 µs |
| kest (`proto`) | hello | 64 | 46 807 | 1 333 µs | 2 042 µs |
| RoadRunner | hello | 64 | 46 337 | 1 310 µs | 2 347 µs |
| kest (`json`) | form | 16 | 38 458 | 399 µs | 792 µs |
| kest (`proto`) | form | 16 | 37 587 | 401 µs | 862 µs |
| RoadRunner | form | 16 | 42 911 | 356 µs | 669 µs |
| kest (`json`) | form | 64 | 42 587 | 1 461 µs | 2 669 µs |
| kest (`proto`) | form | 64 | 41 242 | 1 540 µs | 2 079 µs |
| RoadRunner | form | 64 | 41 255 | 1 459 µs | 2 729 µs |
| kest (`json`) | body-1MB | 16 | 2 592 | 6 110 µs | 7 915 µs |
| kest (`proto`) | body-1MB | 16 | 2 504 | 6 241 µs | 8 445 µs |
| RoadRunner | body-1MB | 16 | 2 415 | 6 342 µs | 11 539 µs |
| kest (`json`) | body-1MB | 64 | 2 683 | 23 362 µs | 32 893 µs |
| kest (`proto`) | body-1MB | 64 | 2 535 | 23 981 µs | 61 544 µs |
| RoadRunner | body-1MB | 64 | 2 536 | 24 680 µs | 32 639 µs |

No request failed in any run. Server memory (RSS) after all the scenarios, the 1 MB bodies last: kest 171 MB,
RoadRunner 367 MB (169 MB and 359 MB without the extension).

**What a codec costs PHP.** [`test/bench/codec.php`](../test/bench/codec.php) runs `PSR7Worker` against a fake relay,
so without I/O: it reads the head of `GET /?name=bench` with ten headers and answers it; `run.sh` prints the result
first.

| Codec | PHP per request | Head |
|---|---|---|
| `json` | 14.4 µs | 716 bytes |
| `proto` with `ext-protobuf` | 20.1 µs | 584 bytes |
| `proto` without `ext-protobuf` | 135 µs | 584 bytes |

**CPU per request** (`ps` CPU time of the server and of its four workers around a single 10 s run with 16 clients,
so rough):

| Target | hello: server | hello: PHP | form: server | form: PHP |
|---|---|---|---|---|
| kest (`json`) | 86 µs | 38 µs | 105 µs | 47 µs |
| kest (`proto`) | 77 µs | 44 µs | 95 µs | 55 µs |
| RoadRunner | 72 µs | 42 µs | 79 µs | 51 µs |

## Reading the numbers

- **With `ext-protobuf`, the codecs are close:** `json` is 1–4 % ahead of `proto` on small requests. Both decoders
  are C, but `json_decode` returns the arrays that `roadrunner/http` builds the PSR-7 request from, while a decoded
  protobuf message is turned into arrays in PHP code, header by header, and the answer is built from message objects
  before it is encoded. That is the 6 µs between the two in the codec table.
- **Without the extension, `proto` is 24–37 % behind `json`:** the pure-PHP `google/protobuf` decodes the head in
  PHP code, at about 135 µs per request. This is the gap `roadrunner/http` warns about ("Without it, performance may
  be up to 50% lower"): protobuf without the extension against protobuf with it. RoadRunner v3 sends protobuf
  only, so the extension gives it 44–60 % more small requests here, and kest's `proto` 37–50 %. `json` doesn't
  change.
- **`json` costs Go more and PHP less.** kest's server spends about 9 µs more per request with `json` than with
  `proto` (`encoding/json` against a hand-written protobuf encoder), PHP 6–8 µs less. Server and PHP together are
  even, but the four PHP workers are the bottleneck and the Go side runs on every core, so `json` comes out ahead.
- **kest and RoadRunner with the same codec:** even on `hello` and with 64 clients. On forms with 16 clients
  RoadRunner is about 8 % ahead, which a second run that took turns between the targets (5 times 15 s each)
  confirmed. kest's server spends more CPU per request than RoadRunner's, on forms most (95–105 µs against 79 µs),
  and on one laptop the load generator, the server and PHP share the cores, so the server's CPU likely comes out of
  PHP's. Finding and trimming kest's share is K-149.
- **64 clients add latency, not throughput:** four workers are busy, and requests wait in the queue.
- **Large bodies:** the codec makes no difference beyond the noise, and kest's memory stays at half of
  RoadRunner's. The 61 ms p99 of kest's `proto` at 64 clients is one run's tail; the control run had 28 ms.
- **Which codec:** keep `json`, the default, unless the PHP side stops speaking JSON (`roadrunner/worker` says its
  v4 will use protobuf by default): it is at least as fast as `proto` with the extension, much faster without it,
  and also works with `roadrunner/http` 2.x–3.4. With `proto`, install `ext-protobuf`.
- These are numbers for one laptop and a minimal worker; run `make bench` on your hardware with your
  worker's typical requests before drawing conclusions for production.

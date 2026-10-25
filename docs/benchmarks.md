# Benchmarks

kest against RoadRunner, serving the same PHP worker on the same machine. Run it yourself with
`RR=/path/to/rr make bench` (after `make php-deps`); the configs and the load generator are in
[`test/bench`](../test/bench).

## Method

- **Worker:** [`examples/php-hello`](../examples/php-hello), an unmodified `roadrunner/http` PSR-7 loop, 4 workers,
  no recycling (`max_jobs` 0), no filters or middleware.
- **Targets:** kest with the `json` codec (its default), kest with the `proto` codec (what RoadRunner sends), and
  RoadRunner 3.0.1 (the release binary, `server.relay: pipes`). Each target runs alone.
- **Scenarios:** `hello` is `GET /?name=bench` (a 41-byte text response); `form` is a `POST /info` with a small
  form that PHP parses and echoes as JSON; `body-1MB` is a `POST /info` with a 1 MB raw body.
- **Load:** [`loadgen`](../test/bench/loadgen), 16 or 64 keep-alive clients, 3 s warm-up, then 15 s measured. Each
  scenario runs 3 times; the table shows the run with the median throughput. Latencies are per request, measured
  by the client.
- **Machine:** an Apple M3 Pro laptop (12 cores, 18 GB), macOS 26.0.1, PHP 8.5.12 (NTS; OPcache off, the CLI
  default, so no JIT), Go 1.27.3; kest at `f159a53` (M3), RoadRunner 3.0.1. The load generator runs on the same
  machine and takes part of the CPU, as it would for any of the targets. Run on 2026-10-25.

## Results

| Target | Scenario | Clients | Requests/s | p50 | p99 |
|---|---|---|---|---|---|
| kest (`json`) | hello | 16 | 48 944 | 307 µs | 652 µs |
| kest (`proto`) | hello | 16 | 34 130 | 455 µs | 712 µs |
| RoadRunner | hello | 16 | 33 331 | 469 µs | 728 µs |
| kest (`json`) | hello | 64 | 50 381 | 1 245 µs | 1 720 µs |
| kest (`proto`) | hello | 64 | 33 833 | 1 866 µs | 2 391 µs |
| RoadRunner | hello | 64 | 33 876 | 1 851 µs | 2 367 µs |
| kest (`json`) | form | 16 | 40 520 | 377 µs | 743 µs |
| kest (`proto`) | form | 16 | 28 526 | 541 µs | 854 µs |
| RoadRunner | form | 16 | 28 543 | 543 µs | 877 µs |
| kest (`json`) | form | 64 | 44 331 | 1 429 µs | 1 944 µs |
| kest (`proto`) | form | 64 | 28 532 | 2 218 µs | 3 072 µs |
| RoadRunner | form | 64 | 28 800 | 2 173 µs | 2 735 µs |
| kest (`json`) | body-1MB | 16 | 2 694 | 5 915 µs | 7 297 µs |
| kest (`proto`) | body-1MB | 16 | 2 591 | 6 163 µs | 7 165 µs |
| RoadRunner | body-1MB | 16 | 2 515 | 6 347 µs | 7 600 µs |
| kest (`json`) | body-1MB | 64 | 2 816 | 22 601 µs | 25 245 µs |
| kest (`proto`) | body-1MB | 64 | 2 686 | 23 716 µs | 26 538 µs |
| RoadRunner | body-1MB | 64 | 2 540 | 25 109 µs | 28 183 µs |

No request failed in any run. Server memory (RSS) after all the scenarios, the 1 MB bodies last: kest 169 MB,
RoadRunner 383 MB.

## Reading the numbers

- **With the same codec, kest and RoadRunner are even** (`proto` against RoadRunner: within 2.5 % on small
  requests, 3–6 % ahead with 1 MB bodies). The four PHP workers are the bottleneck, so the server in front of them
  matters little.
- **kest's default codec makes PHP faster.** With `json`, kest serves 42–54 % more small requests than RoadRunner:
  without the `protobuf` extension, PHP decodes a JSON request head much faster than a protobuf one (about 30 µs
  against 140 µs per request, measured in M2). The difference shrinks as the application does more per request.
- **64 clients add latency, not throughput:** four workers are busy, and requests wait in the queue.
- **Large bodies:** kest holds a body in memory twice while it goes to a worker (K-069); at 1 MB that doesn't
  show in throughput, and kest's memory stays at half of RoadRunner's.
- These are numbers for one laptop and a minimal worker; run `make bench` on your hardware with your
  worker's typical requests before drawing conclusions for production.

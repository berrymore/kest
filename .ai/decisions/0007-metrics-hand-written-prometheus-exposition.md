# ADR-0007: Expose metrics with a hand-written Prometheus text exposition

- **Status:** Accepted (2026-10-25)
- **Date:** 2026-10-25
- **Deciders:** user, agent

## Context

M3 needs metrics (Q-E, K-060): requests by listener, route, cluster and status; latency; pool queue and wait
time; workers by state; restarts by reason; worker memory; the Go runtime and the kest process. The exit
criterion is "enough to build a useful dashboard", scraped by Prometheus from the admin server's `GET /metrics`
(architecture.md § 8).

Constraints: stdlib first, few dependencies, each one approved by the user (ADR-0005); everything bounded,
label cardinality included; the request path stays cheap.

Facts:

- Prometheus 3 negotiates the plain text format 0.0.4 by default: when `scrape_native_histograms` is false, the
  default `scrape_protocols` are `OpenMetricsText1.0.0, OpenMetricsText0.0.1, PrometheusText1.0.0,
  PrometheusText0.0.4` ✅ ([configuration](https://prometheus.io/docs/prometheus/latest/configuration/configuration/),
  2026-10-25).
- RoadRunner's metrics plugin uses `prometheus/client_golang` v1.25.0 ✅
  ([go.mod](https://github.com/roadrunner-server/metrics/blob/master/go.mod), 2026-10-25). Its HTTP worker
  gauges carry a `pid` label ✅ ([metrics.go](https://github.com/roadrunner-server/http/blob/2cd7ee00e9ace25f53872bc86584848bf3d4232e/metrics.go)),
  so the number of series grows with every replaced worker.
- kest's label values come from the config (listener, route, cluster) or from small fixed sets (status code,
  worker state, restart reason). No label value comes from a request.

### Experiment (2026-10-25)

The same program for each option: an HTTP server with a counter of 4 labels, a histogram of 3 labels, a gauge
function and `/metrics`. Built with Go 1.27.3, `-trimpath`, darwin/arm64; kest itself is 11.1 MB. The benchmark is
one counter increment plus one histogram observation, on one goroutine and on 8 goroutines hitting the same series.

| Option | Binary vs. stdlib-only (9.4 MB) | Modules linked | 1 goroutine | 8 goroutines, one series |
|---|---|---|---|---|
| `prometheus/client_golang` v1.25.0, series resolved once | +3.9 MB | 8 (9 on Linux: `procfs`), incl. `google.golang.org/protobuf` | 6.6 ns | 235 ns |
| the same, `WithLabelValues` per request | | | 106 ns | 160 ns |
| `VictoriaMetrics/metrics` v1.44.1 | +0.7 MB | 3 | 3.7 ns | 160 ns |
| OpenTelemetry SDK v1.47.0 + Prometheus exporter v0.69.0 | +5.7 MB | 20, `client_golang` among them | 280 ns, 2 allocs | 140–215 ns |
| hand-written prototype (187 lines) | ~0 | 0 | 4.9 ns | 175–210 ns |

The prototype (counter, gauge function, histogram with fixed buckets, text exposition with escaping) wrote
output that `github.com/prometheus/common/expfmt`, Prometheus's own parser, read back exactly, including quotes,
backslashes and newlines in label values and help texts.

At 30 000 requests per second the worst case above costs under 1 % of one core, so speed doesn't decide this.
Dependencies, binary size and what each option makes us maintain do.

## Decision

We will write the metrics core ourselves, in `internal/metrics`, and expose it in the Prometheus text format 0.0.4.

1. **Types:** a registry of families (name, help, type, fixed label names), counters, gauges (set or read from a
   function at scrape time) and histograms with fixed buckets. Updates are lock-free atomics.
2. **Bounded series.** A component resolves its series when the graph is built, from config values and fixed
   sets; there is no lookup by label values on the request path. Workers are labeled by slot, not by pid.
3. **Exposition:** `GET /metrics` on the admin server, `Content-Type: text/plain; version=0.0.4; charset=utf-8`,
   gzip when the scraper accepts it. Families sorted by name, each with `# HELP` and `# TYPE`.
4. **Go runtime and process metrics:** a curated subset, under the names and with the meaning that
   `client_golang` gives them (`go_goroutines`, `process_cpu_seconds_total`, `process_resident_memory_bytes`,
   `process_open_fds`, `process_start_time_seconds`, …), from `runtime/metrics` and the OS. The list is part of
   K-061.
5. **Tests use Prometheus's parser as the oracle:** `github.com/prometheus/common/expfmt` as a **test-only**
   dependency (Apache-2.0), like `goleak`. It never reaches the binary. Approved by the user with this ADR.
6. **Reversible:** components use only `internal/metrics`. Moving the backend to `client_golang` later changes
   that one package.

## Alternatives considered

- **`prometheus/client_golang`** — the standard: OpenMetrics, protobuf, native histograms, exemplars, complete
  Go and process collectors, maintained by the Prometheus team, used by RoadRunner. Costs +3.9 MB (+35 % for kest)
  and 8–9 modules, including protobuf (kest encodes its protobuf by hand) and two small packages without
  releases since 2019 (`beorn7/perks`, `munnerz/goautoneg`). Its features beyond the text format are ones kest's
  dashboards don't need yet. The choice to make if native histograms or exemplars become a requirement.
- **`VictoriaMetrics/metrics`** — small and fast (MIT, 3 modules, +0.7 MB), process metrics included. But series
  are named by strings with the labels inside (`name{a="x"}`), parsed and checked at runtime; help texts are
  empty; metadata output is a global switch, and there is a global default set. What it would save us is the
  ~200 lines of the core.
- **OpenTelemetry metrics SDK** — the largest (+5.7 MB, 20 modules), allocates on every update, and serving
  Prometheus still goes through `client_golang`. Reconsider only if OTLP push is needed; tracing (later) is a
  separate decision.
- **Hand-written without an oracle** — no test dependency, but then the format is checked only against our own
  reading of the spec.

## Consequences

- Positive: no runtime dependency and no binary growth; cheap updates; cardinality bounded by construction; one
  metrics API for every component and extension.
- Negative / costs: we own the exposition (estimate: 600–800 lines with tests, including the runtime and process
  metrics); no native histograms, exemplars, OpenMetrics or protobuf until we write them or switch backends;
  dashboards built on `client_golang`'s Go and process metrics work only for the subset we export.
- Follow-ups: K-061 (metric set and names), K-062 (`/metrics`), the ADR-0005 table, architecture.md § 8 and the
  Q-E row.

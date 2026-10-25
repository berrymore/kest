# Metrics

kest serves its metrics on the admin server at `GET /metrics`, in the Prometheus text format (version 0.0.4),
gzipped when the scraper asks for it. Prometheus scrapes it with the defaults:

```yaml
scrape_configs:
  - job_name: kest
    static_configs:
      - targets: ["127.0.0.1:9901"]   # the admin address
```

The admin API has no authentication: keep it on a loopback or private address.

## Labels

Every label value comes from the config or from a small fixed set, never from a request, so the number of series
stays bounded:

- `listener`, `virtual_host`, `cluster`: names from the config. Routes have no names, so requests are counted by
  virtual host and cluster. `cluster` is empty for `direct_response` routes and for requests that match no route;
  `virtual_host` is empty when no virtual host matches the request's host.
- `code`: the HTTP status code of the response.
- `state`, `reason`: the fixed values listed below.
- `slot`: a worker's position in its pool, `0` to `workers - 1`. Workers are not labeled by pid, so replacing them
  doesn't add series.

## HTTP

| Metric | Type | Labels | |
|---|---|---|---|
| `kest_http_requests_total` | counter | `listener`, `virtual_host`, `cluster`, `code` | Requests, counted when the response is done. |
| `kest_http_request_duration_seconds` | histogram | `listener`, `virtual_host`, `cluster` | From routing the request until its handler returns: the wait for a worker and the worker's time. Buckets from 5 ms to 60 s. |
| `kest_http_requests_in_flight` | gauge | `listener` | Requests being served now. |

## TLS

For each listener with `tls`:

| Metric | Type | Labels | |
|---|---|---|---|
| `kest_tls_certificate_expiry_timestamp_seconds` | gauge | `listener`, `cert_file` | When each certificate expires, as a Unix timestamp; it moves when a renewed certificate is loaded. |
| `kest_tls_handshake_errors_total` | counter | `listener` | TLS handshakes that failed: clients that don't trust the certificate, scanners, plain HTTP on the TLS port. |

## Worker pools

For each `php_workers` cluster:

| Metric | Type | Labels | |
|---|---|---|---|
| `kest_pool_workers` | gauge | `cluster`, `state` | Worker slots by state: `starting`, `ready`, `working`, `stopping`, `backoff` (waiting to restart after failures), `stopped`. |
| `kest_pool_queue_length` | gauge | `cluster` | Requests waiting for a free worker. |
| `kest_pool_buffered_body_bytes` | gauge | `cluster` | Request body bytes reserved now, out of `max_buffered_bodies`. |
| `kest_pool_wait_seconds` | histogram | `cluster` | How long requests waited for a free worker. |
| `kest_pool_rejected_total` | counter | `cluster`, `reason` | Requests answered `503` without a worker: `queue_full` (`pool.max_queue_size` requests were waiting already), `no_worker` (none became free within `pool.allocate_timeout`) or `body_budget` (`max_buffered_bodies` was full). |
| `kest_pool_worker_restarts_total` | counter | `cluster`, `reason` | Workers replaced: `max_jobs`, `ttl`, `idle_ttl`, `memory` (over `supervisor.max_worker_memory`), `exec_ttl` (killed in a request that ran too long), `crash` (exited or broke the protocol), `stop_requested` (the worker asked for it), `start_failed` (a new worker didn't boot), `reset` (a rolling reset). |
| `kest_pool_worker_memory_bytes` | gauge | `cluster`, `slot` | Each worker's resident memory, as last sampled. Sampled only when `supervisor.max_worker_memory` is set, every `supervisor.watch_tick`. |

## Go runtime and process

The same names and meaning as in Prometheus's Go client, so existing panels for Go services work:
`go_info`, `go_goroutines`, `go_threads`, `go_sched_gomaxprocs_threads`, `go_memstats_alloc_bytes`,
`go_memstats_sys_bytes`, `go_memstats_next_gc_bytes`, `go_gc_gogc_percent`, `go_gc_gomemlimit_bytes`,
`process_cpu_seconds_total`, `process_open_fds`, `process_max_fds`, `process_start_time_seconds`, and on Linux
`process_resident_memory_bytes` and `process_virtual_memory_bytes`. They describe kest itself, not the workers.

## Example queries

```promql
# Requests per second, by cluster
sum by (cluster) (rate(kest_http_requests_total[5m]))

# Share of 5xx responses, by cluster
sum by (cluster) (rate(kest_http_requests_total{code=~"5.."}[5m]))
  / sum by (cluster) (rate(kest_http_requests_total[5m]))

# 99th percentile latency, by cluster
histogram_quantile(0.99, sum by (cluster, le) (rate(kest_http_request_duration_seconds_bucket[5m])))

# Pool saturation: busy workers out of all live ones
sum by (cluster) (kest_pool_workers{state="working"})
  / sum by (cluster) (kest_pool_workers{state=~"ready|working"})

# Requests turned away because the pool was full or too slow
sum by (cluster, reason) (rate(kest_pool_rejected_total[5m]))

# Certificates that expire within 14 days (renewal didn't happen)
kest_tls_certificate_expiry_timestamp_seconds - time() < 14 * 86400

# Worker restarts that are not routine
sum by (cluster, reason) (increase(kest_pool_worker_restarts_total{reason=~"crash|exec_ttl|memory|start_failed"}[1h]))
```

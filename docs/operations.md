# Operations guide

How to run kest in production: processes and signals, probes, deploys, sizing, monitoring and
troubleshooting. Option details are in the [configuration reference](config.md), metrics in
[Metrics](metrics.md).

## Running kest

`kest serve -c kest.yaml` runs in the foreground and logs to stderr; check a config first with
`kest validate -c kest.yaml` (add `-dump` to see every default). kest has no daemon mode: run it under a
supervisor. A systemd unit:

```ini
[Unit]
Description=kest application server
After=network.target

[Service]
ExecStart=/usr/local/bin/kest serve -c /etc/kest/kest.yaml
ExecReload=/bin/kill -HUP $MAINPID
# drain_delay + 2 × drain_timeout + a little: kest stops listeners, then clusters
TimeoutStopSec=60
Restart=on-failure
User=www-data
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

`systemctl reload kest` then replaces the PHP workers one by one; it doesn't reload the configuration (see
[Deploys](#deploys)). In a container, run kest as the entrypoint so that it gets the signals, and read its logs
from stderr.

Workers run as kest's user, in their own process group, with kest's environment unless `inherit_env: false`.
On Linux they die with kest even if kest is killed with SIGKILL.

## Signals

| Signal | Effect |
|---|---|
| SIGTERM, SIGINT | Graceful shutdown: `/ready` answers `503 draining`; the listeners keep serving for `server.drain_delay`; then they stop accepting and let running requests finish within `server.drain_timeout`; the clusters stop their workers within the same budget; the admin server stops last. |
| a second SIGTERM or SIGINT | Exit at once. |
| SIGHUP | A rolling reset of every cluster's workers, as `POST /clusters/{name}/reset`; the outcome is logged. |

## Probes

- **Startup:** kest boots every worker before it opens the admin port and the listeners, so until then probes
  get "connection refused". That can take up to `pool.start_timeout` (60s by default) if your application boots
  slowly: give probes an initial delay, or a startup probe, at least that long.
- **Readiness: `GET /ready`** on the admin address. `200 ready` while kest serves; `503 starting` for the moment
  between the admin port and the listeners opening, `503 draining` once shutdown begins, and
  `503 not serving: <clusters>` while a cluster has no live worker at all. A pool that is only replacing some
  workers still counts as serving. Point load balancers here, and set `server.drain_delay` a little above their
  check interval, so that they stop sending traffic before the listeners close.
- **Liveness:** `GET /server_info` answers as long as the process serves, once it has started. Don't use
  `/ready` for liveness: a restart doesn't fix a cluster whose code fails to boot.

The admin API has no authentication. Keep it on `127.0.0.1` or a private network.

## Deploys

A PHP worker loads your code once, so a deploy needs new workers:

1. Put the new code in place. If you switch a `current` symlink between releases, keep the config's path (or
   the cluster's `dir`) under the symlink: new workers start there and get the new release.
2. `curl -fsS -X POST localhost:9901/clusters/<cluster>/reset` (or `systemctl reload kest` for every cluster).

The reset replaces one worker at a time: each new one boots while the old one keeps serving, takes over between
two requests, and then the old one stops. The pool keeps its capacity, and requests don't fail. The call returns
when every worker is replaced: `200 reset done`. If a new worker doesn't boot (a syntax error, a missing
dependency), the reset stops there and the call answers `500` with the worker's error: the workers it didn't
reach still run the old code and keep serving, so roll the code back and reset again. `409` means a reset is
running already.

Configuration changes need a restart of kest; reloading the config is planned (M6).

In development, a cluster's `reload` block resets the workers by itself when files change. Leave it off in
production: deploys should decide when workers switch to new code.

## Sizing

- **`pool.workers`**: PHP runs one request per worker. CPU-bound applications want about one worker per core;
  applications that wait on databases or APIs want more. Start with 2× cores and watch the pool saturation
  query in [Metrics](metrics.md).
- **Waiting:** a request waits for a free worker up to `pool.allocate_timeout`, and at most
  `pool.max_queue_size` requests wait; others get `503` at once. Keep the queue short enough that a waiting
  request still finishes within its route `timeout`.
- **Leaks:** `pool.max_jobs` and `supervisor.max_worker_memory` replace workers that grow; `supervisor.ttl`
  replaces them by age. Restarts for these reasons are routine.
- **Stuck requests:** `supervisor.exec_ttl` (default 60s) kills a worker stuck on one request; the route
  `timeout` (default 30s) answers the client `504` earlier, while the worker finishes.
- **Memory:** about `pool.workers` × the worker's RSS, plus kest itself (about 25 MB), plus request bodies,
  which `max_buffered_bodies` bounds per cluster.
- **Isolation:** a slow part of an application can get its own cluster (a second pool) and route, so that it
  can't take every worker.

## Monitoring

Scrape `GET /metrics` (Prometheus text format). Worth an alert:

- the share of `5xx` in `kest_http_requests_total`;
- `kest_pool_rejected_total` growing: requests turned away because the pool or the body budget was full;
- `kest_pool_worker_restarts_total` for `crash`, `exec_ttl`, `memory` and `start_failed`;
- pool saturation (`working` out of `ready` + `working` workers) close to 1 for a while;
- `/ready` failing.

[Metrics](metrics.md) has the queries.

## Logs

Logs go to stderr, as text or JSON (`log.format`). Lines from a worker's stderr are logged with `cluster` and
`worker_pid`; PHP's `echo` in a worker goes there too. Turn on the `access_log` filter for a line per request,
and `request_id` to find a request's line, its PHP logs (the `X-Request-Id` header) and the client's report.
Failed requests are logged with their status: `5xx` at error or warn level, client errors at debug.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| kest exits at start: `stdout is reserved for the RoadRunner protocol, but the worker printed "…"` | The script doesn't run a RoadRunner worker loop, or it printed something before it: a PHP warning (set `display_errors=stderr`), an `echo`, or for Symfony a missing `APP_RUNTIME` in the cluster's `env`. |
| kest exits at start with the worker's exit status and stderr | The application fails to boot; the message shows PHP's error. |
| `503` | No worker free in time (`no_worker`), the queue full (`queue_full`) or the body budget full (`body_budget`): see `kest_pool_rejected_total`. Or `/ready` while a cluster has no live worker. |
| `504` | The route `timeout` expired; the worker may still be running the request (until `exec_ttl`). |
| `502` | The worker died in the request (a fatal error, `exit`), broke the protocol, or sent a response over `max_response_size`; the log has the details. |
| `500` | The application reported an error (`$worker->getWorker()->error()`), or a panic in kest (with `recovery`). |
| `408`, `413`, `431` | The body arrived too slowly, was over `max_request_body`, or the headers were over `max_header_bytes`. |
| workers in `backoff` | They keep failing to start or die early; kest waits up to 30s between attempts. The log and `start_failed` restarts show why. |

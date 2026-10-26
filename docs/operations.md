# Operations guide

How to run kest in production: processes, the proxy in front, TLS, signals, probes, deploys, sizing, isolating
slow endpoints, overload, monitoring and troubleshooting. Option details are in the [configuration reference](config.md), metrics in
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
# /run/kest for a unix socket listener: /run is emptied at boot
RuntimeDirectory=kest
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

`systemctl reload kest` then replaces the PHP workers one by one; it doesn't reload the configuration (see
[Deploys](#deploys)). In a container, run kest as the entrypoint so that it gets the signals, and read its logs
from stderr.

Workers run as kest's user, in their own process group, with kest's environment unless `inherit_env: false`.
On Linux they die with kest even if kest is killed with SIGKILL.

## Behind a proxy

kest is an application server: it runs behind nginx, a Kubernetes ingress or a load balancer, which faces the
internet (certificates for browsers, files for CDNs, balancing over hosts). With nginx on the same host:

```nginx
upstream kest {
    server 127.0.0.1:8080;
    keepalive 32;                     # reuse connections to kest
}

server {
    listen 443 ssl;
    server_name example.com;
    # ssl_certificate, ssl_certificate_key …

    location / {
        proxy_pass http://kest;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

- Add the `client_ip` filter with the proxy's addresses in `trusted_proxies` (here `127.0.0.1`), so that PHP, the
  access log and your rate limits see the client instead of the proxy, and so that PHP builds `https://` URLs when
  the proxy terminates TLS (`X-Forwarded-Proto`). Without it, PHP sees the proxy's address. Don't also configure
  your framework's trusted proxies with the proxy's address: PHP already sees the client as the remote address.
- Files: when the proxy can see the files of `public/` (the same host, a shared volume), let it serve them (nginx:
  `root` and `try_files $uri @kest`, keeping `.php` files from being served as files) and leave the `static` filter
  out. An ingress usually can't see the application's container: then set `static` on kest's route to PHP.
- Streamed responses (server-sent events, generators): nginx buffers responses by default, so the client gets the
  stream only at its end. Send `X-Accel-Buffering: no` from PHP for those responses, or set `proxy_buffering off`
  on their location.
- Load balancers that speak HTTP/2 to their backends without TLS (h2c with prior knowledge) can use it: add
  `http2` to the listener's [`protocols`](config.md#listenershttp).

On the same host, nginx can reach kest through a unix socket instead of a port, which no other host can reach and
whose file mode decides which local users may connect:

```yaml
listeners:
  - name: main
    address: unix:/run/kest/kest.sock
    socket_mode: "0660"          # the owner and the group: put nginx's user in kest's group
    http:
      filters:
        - type: client_ip
          config: {trusted_proxies: [unix]}
      # …
```

```nginx
upstream kest {
    server unix:/run/kest/kest.sock;
    keepalive 32;
}
```

The directory must exist and be writable by kest; the systemd unit above creates `/run/kest` with
`RuntimeDirectory`. kest removes a socket file left by a killed kest when it starts,
refuses to start while another process listens on the path, and removes the file when it stops. A peer on a unix
socket has no IP address: without `client_ip` and `unix` in its `trusted_proxies`, PHP sees an empty `REMOTE_ADDR`.

## TLS

The proxy in front usually terminates TLS. A listener with a `tls` block serves HTTPS itself
([`listeners[].tls`](config.md#listenerstls)), for when nothing in front does: development, a load balancer that
passes TCP through, traffic that must stay encrypted inside the network. With Let's Encrypt:

- Point `cert_file` at `fullchain.pem` (certificate and intermediates) and `key_file` at `privkey.pem`. kest picks
  up a renewed pair by itself within about 20 seconds: no reload hook, no restart. `kest_tls_certificate_expiry_timestamp_seconds`
  tells you when renewal didn't happen ([Metrics](metrics.md#tls)).
- The private key must be readable by kest's user. certbot's `live/` directory is root-only: give the group of
  kest's user read access to `archive/` and `live/`, or copy the files elsewhere in a deploy hook.
- Ports below 1024 need `AmbientCapabilities=CAP_NET_BIND_SERVICE` in the systemd unit when kest runs as an
  unprivileged user.
- Send plain HTTP to HTTPS with a second listener on port 80 whose only route is
  `{match: {prefix: /}, redirect: {scheme: https}}`, and tell browsers to stay on HTTPS with the `headers` filter:
  `response: {set: {Strict-Transport-Security: max-age=63072000}}`.

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

Configuration changes need a restart of kest; reloading the config is a candidate for a later milestone (M8).

In development, a cluster's `reload` block resets the workers by itself when files change. Leave it off in
production: deploys should decide when workers switch to new code.

## Sizing

- **`pool.workers`**: PHP runs one request per worker. CPU-bound applications want about one worker per core;
  applications that wait on databases or APIs want more. Start with 2× cores and watch the pool saturation
  query in [Metrics](metrics.md).
- **`pool.max_workers`** lets the pool grow while requests wait and shrink after `scale_down_after`: memory for
  quiet hours, workers for busy ones. A new worker boots the framework (often 100 ms to 1 s, and CPU-heavy), so it
  helps with load that lasts, not with a spike of a second; kest boots at most `workers`/4 at once. Size `workers`
  for the usual load and `max_workers` for the memory you can give at the peak.
- **Waiting:** a request waits for a free worker up to `pool.allocate_timeout` (or its route's `queue_timeout`), and
  at most `pool.max_queue_size` requests wait in each lane; others get `503` at once. Keep the wait short enough
  that a waiting request still finishes within its route `timeout`. Under overload, [overload control](#overload)
  shortens it.
- **Leaks:** `pool.max_jobs` and `supervisor.max_worker_memory` replace workers that grow; `supervisor.ttl`
  replaces them by age. Restarts for these reasons are routine.
- **Stuck requests:** `supervisor.exec_ttl` (default 60s) kills a worker stuck on one request; the route
  `timeout` (default 30s) answers the client `504` earlier, while the worker finishes.
- **kest's own CPU:** a few tens of microseconds per request. On a host that kest shares with its PHP workers,
  run it with `GOMAXPROCS=2` to `4` (an environment variable of the Go runtime): Go otherwise runs a thread per
  core, and the idle ones spin and take CPU from PHP. On a laptop with 12 cores and 4 workers, that served 30 %
  more requests ([Benchmarks](benchmarks.md#kests-own-cpu)). Give TLS, HTTP/2 and `compression` more.
- **Memory:** about `pool.workers` × the worker's RSS (`pool.max_workers` × at the peak of a pool that grows), plus
  kest itself (about 25 MB), plus request bodies, which `max_buffered_bodies` bounds per cluster.
- **Isolation:** see the next section.

## Isolating slow endpoints

One pool serves the whole application, so a slow endpoint (exports, reports, uploads to a remote store) can take
every worker when many requests hit it at once, and the rest of the site waits behind it. Give its route a
**lane**: a queue of its own in the pool, with a limit on the workers it takes.

```yaml
routes:
  - name: export
    match: {prefix: /export/}
    route: {cluster: app, timeout: 120s, max_workers: 2, priority: low, queue_timeout: 30s}
  - name: checkout
    match: {prefix: /checkout/}
    route: {cluster: app, priority: high}
  - match: {prefix: /}
    route: {cluster: app}
```

- **`max_workers`** keeps the export to 2 of the pool's workers however many requests come; the others wait in
  its own queue, which doesn't fill the queue of the other routes. Measured with a pool of 4 PHP workers and 8
  clients on a route that sleeps 500 ms: the other routes' p99 stayed under 1 ms with the lane, and went to 0.8–1 s
  without it (`test/e2e/php_lanes_test.go`).
- **`priority`** decides who gets a free worker first when requests wait: `high` lanes before `normal` ones (the
  default lane is `normal`) before `low` ones. It is strict, so under sustained overload low lanes get no worker;
  their `queue_timeout` then answers `503` quickly, with `Retry-After`, instead of letting their requests wait
  minutes.
- **`queue_timeout`** bounds the wait for a worker per route, in place of the pool's `allocate_timeout`: short for
  an API whose clients give up fast, longer for a job that is worth waiting for.
- A separate cluster (a second pool with its own `command`, `env` or limits) isolates even more: memory limits,
  `max_jobs` and boot time of its own. It costs workers that sit idle while the other pool is busy; a lane only
  caps what a route can take from a shared pool.

Watch the lanes with `kest_pool_wait_seconds`, `kest_pool_queue_length`, `kest_pool_lane_workers` and
`kest_pool_rejected_total`, labeled by the route's name (`route=""` is the default lane), and in `/clusters`.

## Overload

When more requests come than the pool serves, a queue that only grows makes everything worse: requests wait as long
as their clients do, and the workers serve requests whose clients have already given up. kest's overload control
(`pool.overload`, on by default) sees a lane in which requests have waited without a break for 5 s and from then on
lets its requests wait at most 500 ms; those that get no worker by then get `503` with `Retry-After`, and the served
ones get a worker quickly. Below the pool's capacity it never acts. [Benchmarks](benchmarks.md#overload-control) has the
measurements: with requests of 100 ms at twice the capacity, a pool without it served 7 requests per second of 40.

- Lanes with `max_workers` or `queue_timeout` are left out: their waits are what the route asks for.
- An application whose requests take seconds may see its queue go 5 s without a break under normal load: raise
  `overload.interval` and `overload.queue_timeout` (keep the timeout around a few typical requests), or turn it off
  with `overload: {queue_timeout: 0s}`.
- `kest_pool_rejected_total{reason="overload"}` counts what it turned away. Requests turned away because the pool
  is too small call for more workers or more instances, not for a longer queue.

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
| `503` | No worker free in time (`no_worker`), the queue full (`queue_full`), overload control (`overload`, see `pool.overload`) or the body budget full (`body_budget`): see `kest_pool_rejected_total`. Or `/ready` while a cluster has no live worker. |
| `504` | The route `timeout` expired; the worker may still be running the request (until `exec_ttl`). |
| `502` | The worker died in the request (a fatal error, `exit`), broke the protocol, or sent a response over `max_response_size`; the log has the details. A streamed response that fails after its headers is cut off instead. |
| `500` | The application reported an error (`$worker->getWorker()->error()`), or a panic in kest (with `recovery`). |
| `408`, `413`, `431` | The body arrived too slowly, was over `max_request_body`, or the headers were over `max_header_bytes`. |
| workers in `backoff` | They keep failing to start or die early; kest waits up to 30s between attempts. The log and `start_failed` restarts show why. |

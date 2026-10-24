# Getting started

kest runs PHP applications through long-lived worker processes, like RoadRunner: each worker boots
your application once and then serves requests in a loop. The workers are ordinary RoadRunner workers
(`roadrunner/http`, formerly `spiral/roadrunner-http`), so code written for RoadRunner runs unchanged.

This guide builds a small PSR-7 application from scratch. The finished version is in
[`examples/php-hello`](../examples/php-hello).

## Requirements

- **kest.** Build it from a checkout with Go 1.27+: `make build` puts the binary in `bin/kest`
  (or `go install github.com/berrymore/kest/cmd/kest@latest`).
- **PHP 8.2+** with the `sockets` extension (`php -m | grep sockets`), and **Composer**. kest itself
  doesn't care about the PHP version; the RoadRunner libraries need 8.2, and kest is tested on 8.3,
  8.4 and 8.5.

## 1. A PHP worker

```sh
mkdir hello && cd hello
composer require roadrunner/http nyholm/psr7
```

`worker.php`:

```php
<?php

require __DIR__ . '/vendor/autoload.php';

use Nyholm\Psr7\Factory\Psr17Factory;
use Nyholm\Psr7\Response;
use Spiral\RoadRunner\Http\PSR7Worker;
use Spiral\RoadRunner\Worker;

$factory = new Psr17Factory();
$worker = new PSR7Worker(Worker::create(), $factory, $factory, $factory);

while ($request = $worker->waitRequest()) {
    try {
        $name = $request->getQueryParams()['name'] ?? 'world';
        $worker->respond(new Response(200, ['Content-Type' => 'text/plain'], "Hello, $name!\n"));
    } catch (\Throwable $e) {
        $worker->getWorker()->error((string) $e); // kest logs it and answers 500
    }
}
```

## 2. A kest config

`kest.yaml`, next to `worker.php`:

```yaml
version: v1

listeners:
  - name: main
    address: 127.0.0.1:8080
    http:
      virtual_hosts:
        - name: default
          domains: ["*"]
          routes:
            - match: { prefix: / }
              route: { cluster: app }

clusters:
  - name: app
    type: php_workers
    config:
      command: [php, worker.php]
      pool:
        workers: 4
```

Check it, then start kest:

```sh
kest validate -c kest.yaml
kest serve -c kest.yaml
```

```sh
$ curl 'localhost:8080/?name=kest'
Hello, kest!
$ curl localhost:9901/ready
ready
$ curl -s localhost:9901/metrics | grep 'kest_pool_workers{'
kest_pool_workers{cluster="hello",state="starting"} 0
kest_pool_workers{cluster="hello",state="ready"} 4
kest_pool_workers{cluster="hello",state="working"} 0
kest_pool_workers{cluster="hello",state="stopping"} 0
kest_pool_workers{cluster="hello",state="backoff"} 0
kest_pool_workers{cluster="hello",state="stopped"} 0
```

The admin server (`localhost:9901`) also serves [metrics](metrics.md) for Prometheus.

kest starts the four workers before it opens the port; if one can't boot, kest exits with the
worker's error output. Stop it with Ctrl-C or SIGTERM: running requests finish first.

`command` runs in the config file's directory (set `dir` to change it). Workers inherit kest's
environment, so `APP_ENV=prod kest serve …` reaches PHP; add variables with `env`. Every option is in
the [configuration reference](config.md#php_workers).

## 3. Things to know about long-lived workers

- **State survives between requests.** Globals, static properties and services built before the
  loop are shared by every request a worker serves. That is the point (no boot per request), and also
  the most common source of bugs: reset per-request state yourself, or use your framework's reset
  mechanism.
- **Stdout belongs to kest.** It carries the protocol. After `Worker::create()` the library sends
  `echo` and `print` output to stderr, which kest logs. Output before that, such as a PHP warning
  during boot (the PHP CLI prints errors to stdout by default; `display_errors=stderr` changes that),
  makes the worker fail to start, and kest shows what it printed: `stdout is reserved for the
  RoadRunner protocol, but the worker printed "…"`.
- **Code changes need a restart.** A worker loads your code once. Restart kest to deploy; rolling
  worker restarts without downtime are planned (M3).
- **Leaks are bounded by limits.** `pool.max_jobs` replaces a worker after that many requests,
  `supervisor.max_worker_memory` when it grows too large, `supervisor.ttl` after some time. A request
  that runs longer than `supervisor.exec_ttl` (default 60s) kills its worker and gets `504`.
- **Errors.** An uncaught exception that you pass to `$worker->getWorker()->error()` becomes a `500`
  and the worker keeps serving; a fatal error or `exit` ends the worker: the request gets `502` and
  kest starts a new worker. Details are in kest's log, never in the response.
- **Limits are tighter than RoadRunner's.** Request bodies are limited to 16 MB
  (`max_request_body`), at most 1000 requests wait for a free worker (`pool.max_queue_size`), and a
  request waits at most 60s for one (`pool.allocate_timeout`).

## 4. Frameworks

Anything that runs on RoadRunner's HTTP worker should run on kest; kest is tested with:

- **Symfony** 7.4 with [`runtime/roadrunner-symfony-nyholm`](https://github.com/php-runtime/roadrunner-symfony-nyholm):

  ```yaml
  clusters:
    - name: symfony
      type: php_workers
      config:
        command: [php, public/index.php]
        env:
          APP_ENV: prod
          APP_DEBUG: "0"
          APP_RUNTIME: Runtime\RoadRunnerSymfonyNyholm\Runtime
  ```

  (the test app is in [`test/e2e/testdata/symfony`](../test/e2e/testdata/symfony)). Set `APP_RUNTIME` in
  `env` (or `extra.runtime.class` in `composer.json`), not in `.env`: `symfony/runtime` picks the runtime
  before it reads `.env`. Without it the app runs as a plain script, prints a page to stdout, and the worker
  fails to start.

Other integrations (Laravel Octane, Spiral, Yii) haven't been tested yet. Those that need RoadRunner's
RPC plugin, for example to reset workers from PHP or for RoadRunner's KV and jobs plugins, won't work:
kest has no RPC yet.

## 5. Coming from RoadRunner

| `.rr.yaml` | kest | |
|---|---|---|
| `server.command` | `clusters[].config.command` | a list: `[php, worker.php]` |
| `server.env` | `env` | kest also passes its own environment, like RR; `inherit_env: false` turns that off |
| `http.address` | `listeners[].address` | routes then pick a cluster per path and host |
| `http.pool.num_workers` | `pool.workers` | |
| `http.pool.max_jobs` | `pool.max_jobs` | |
| `http.pool.allocate_timeout` | `pool.allocate_timeout` | |
| `http.pool.destroy_timeout` | `pool.destroy_timeout` | RR: 60s default; kest: 10s |
| — | `pool.max_queue_size` | RR's queue is unbounded by default; kest's holds 1000 |
| `http.pool.supervisor.*` | `supervisor.*` | `max_worker_memory` is a size (`256MB`), not a number of MB; `exec_ttl` defaults to 60s (RR: unlimited) |
| `http.max_request_size` (MB) | `max_request_body` | a size; default 16MB (RR: 1000 MB) |
| `http.uploads.dir/forbid/allow` | `uploads.dir/forbid/allow` | |
| `http.raw_body` | `raw_body` | |
| `http.internal_error_code` | — | kest answers 500, 502, 503 or 504 depending on what failed |

Not there yet: TLS, HTTP/2, static files and the other HTTP middleware (M4), metrics and rolling
restarts (M3), RPC, jobs, KV and the other RoadRunner plugins. Streaming responses
(`PSR7Worker::$chunkSize`) aren't supported yet either.

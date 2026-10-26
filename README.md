# kest

An Envoy-inspired application server for PHP (and, later, WebAssembly), written in Go.

> **Status: experimental.** Milestones M0–M6 are done: kest serves PHP applications through pools of
> unmodified RoadRunner PSR-7 workers (`roadrunner/http`), behind strictly validated Envoy-style routing, with
> Prometheus metrics, rolling restarts without failed requests, access logs and request IDs, and bounded
> resources. It speaks HTTP/2 (h2c) to the proxy in front of it, takes the client's address from trusted proxies,
> terminates TLS when needed, runs filters per listener, virtual host and route (CORS, headers, gzip, the files of
> `public/`, `X-Sendfile`), redirects, and streams responses from PHP. A pool keeps a slow endpoint to a few of its
> workers in a lane of its own, serves important routes first, turns requests away fast under overload instead of
> letting them pile up, and grows under load. The proxy can reach it over a unix socket. A Symfony app runs on it
> unchanged. No milestone is scheduled next: kest is being shaped by a real application running on it. The goal is
> production quality; the candidates are in [`.ai/roadmap.md`](.ai/roadmap.md).

## Why

[RoadRunner](https://github.com/roadrunner-server/roadrunner) showed that a PHP application can boot once
and then serve thousands of requests from long-lived worker processes managed by a Go server.
[Envoy](https://www.envoyproxy.io/) showed that a server can be described as an explicit, declarative
graph: listeners, filter chains, routes and clusters.

kest puts the two ideas together:

- **Long-lived PHP workers.** The framework boots once per worker, not once per request. The goal is to
  run existing RoadRunner PHP workers (`spiral/roadrunner-http`, PSR-7) without changes.
- **Envoy-style configuration.** Listeners, HTTP filters, virtual hosts, routes and clusters in one YAML
  file, validated strictly before anything starts.
- **Many pools in one process.** Route `/api` and `/reports` to different PHP pools, keep slow endpoints
  from starving fast ones, and give each pool its own limits.
- **Work off PHP's hands.** Filters on a listener, a virtual host or a route act before and after PHP, so that PHP
  keeps the business logic.
- **Behind your proxy, not instead of it.** kest stands behind nginx, an ingress or a load balancer: it speaks
  HTTP/1.1 and h2c to it and takes the client's address from it. Certificates for the internet, CDN-grade files
  and proxying to other services stay with the proxy.
- **WASM sandbox (planned).** Run small, isolated WebAssembly modules for hot paths, as handlers or HTTP
  filters, with hard memory and time limits.
- **Built to be operated.** Admin API, metrics, structured logs, graceful shutdown and rolling restarts.

## What it looks like

`kest.yaml`:

```yaml
version: v1

admin:
  address: 127.0.0.1:9901

listeners:
  - name: public
    address: 0.0.0.0:8080
    http:
      virtual_hosts:
        - name: app
          domains: ["*"]
          routes:
            - match: { prefix: /healthz }
              direct_response: { status: 200, body: "ok" }
            - match: { prefix: /reports/ }
              route: { cluster: php_heavy, timeout: 120s }
            - match: { prefix: / }
              filters:
                - type: static            # files of public/ without PHP
                  config: { dir: public }
              route: { cluster: php_app, timeout: 30s }

clusters:
  - name: php_app
    type: php_workers
    config:
      command: [php, worker.php]
      pool: { workers: 8, max_jobs: 1000 }
      supervisor: { exec_ttl: 30s, max_worker_memory: 256MB }

  - name: php_heavy               # a separate pool for slow endpoints
    type: php_workers
    config:
      command: [php, worker.php]
      pool: { workers: 2 }
```

`worker.php`, a regular RoadRunner PSR-7 worker:

```php
<?php

require __DIR__ . '/vendor/autoload.php';

use Nyholm\Psr7\Factory\Psr17Factory;
use Spiral\RoadRunner\Http\PSR7Worker;
use Spiral\RoadRunner\Worker;

$factory = new Psr17Factory();
$worker = new PSR7Worker(Worker::create(), $factory, $factory, $factory);

while ($request = $worker->waitRequest()) {
    $worker->respond($factory->createResponse(200)->withBody($factory->createStream('Hello from kest')));
}
```

```sh
kest validate -c kest.yaml   # strict validation with precise error paths
kest serve -c kest.yaml
```

HTTP filters (`recovery`, `request_id`, `access_log`, `client_ip`, `headers`, `cors`, `compression`, `static`, `sendfile`) run
in config order: the listener's, then those of the virtual host and of the route that the router picks. Clusters:
`php_workers`.

## Architecture at a glance

```
                ┌──────────────────────────────── kest ─────────────────────────────────┐
                │                                                                       │
  proxy ──────▶ │  listener ──▶ HTTP filters ──▶ router ──▶ cluster                     │
                │   :8080       request_id,      vhosts,    ├─ php_workers ─▶ pool ─────┼──▶ php worker.php × N
                │               access_log, …    routes,    └─ wasm (planned) ─▶ wazero │
                │                                and their                              │
                │                                filters                                │
                │                                                                       │
                │  admin :9901 — /ready, /config_dump, /clusters, /metrics              │
                └───────────────────────────────────────────────────────────────────────┘
```

Details: [`.ai/architecture.md`](.ai/architecture.md).

## Roadmap

| Milestone | Goal |
|---|---|
| **M0** Foundation ✅ | CLI, strict config loader, extension registry, lifecycle, admin `/ready`, `direct_response` |
| **M1** Worker pool ✅ | Language-agnostic process pool, supervisor, RoadRunner-compatible relay |
| **M2** PHP HTTP — *MVP* ✅ | Serve PSR-7 apps through unmodified RoadRunner HTTP workers |
| **M3** Operability ✅ | Metrics, admin API, rolling restarts, access logs, limits, benchmarks |
| **M4** HTTP features ✅ | TLS, HTTP/2 and h2c, trusted proxies, static files, compression, CORS, redirects, streaming |
| **M5** Worker pools and routing ✅ | Lanes per route (worker limits, priorities, queue timeouts), overload control, dynamic pool size, `X-Sendfile`, richer matches |
| **M6** Unix socket listeners ✅ | A unix socket for the proxy in front |
| **M7** PHP → kest RPC (candidate) | The channel RoadRunner's PHP libraries call |
| **M8** Dynamic config (candidate) | Hot reload of the config file, config API |
| **M9** WASM sandbox (candidate) | wazero runtime, `wasm` clusters and filters |
| **M10** gRPC (candidate) | gRPC on the HTTP/2 listener, routed to PHP workers |

## Try it

```sh
make build
composer install --working-dir=examples/php-hello
./bin/kest serve -c examples/php-hello/kest.yaml
curl 'localhost:8080/?name=kest'   # Hello, kest! (PHP 8.5.12, worker 12345)
curl localhost:9901/ready          # admin API: ready
./bin/kest validate -c examples/php-hello/kest.yaml -dump
```

[`examples/hello`](examples/hello) needs no PHP: it only has direct responses. [`examples/php-kv`](examples/php-kv)
keeps the workers' shared state in kest's `kv` plugin, through `roadrunner/kv`.

- [Getting started](docs/getting-started.md): your first PHP app on kest, long-lived workers, Symfony,
  coming from RoadRunner.
- [Configuration reference](docs/config.md).
- [Metrics](docs/metrics.md): what `GET /metrics` reports, with example queries.
- [Operations](docs/operations.md): the proxy in front, TLS, signals, probes, deploys with rolling resets, sizing,
  troubleshooting.
- [Benchmarks](docs/benchmarks.md): kest against RoadRunner with the same PHP worker.

## Development

Requirements:

- Go 1.27+
- PHP CLI and Composer, for integration tests and examples (macOS: `brew install php composer`).
  kest itself doesn't restrict the PHP version; the RoadRunner PHP libraries currently need PHP 8.2+,
  and kest is tested against PHP 8.3+.
- Docker, for Linux test runs and the PHP version matrix

```sh
make check              # build, vet, lint, unit tests (-race), end-to-end tests
make test-integration   # plus the PHP integration and end-to-end tests (needs php and composer)
make test-matrix        # the PHP tests on Linux with PHP 8.3, 8.4 and 8.5 (needs Docker)
make help               # all targets
```

Contributors and AI agents: start with [`AGENTS.md`](AGENTS.md).

## Acknowledgements

kest draws heavily on ideas from RoadRunner (MIT) and Envoy (Apache-2.0). It is not affiliated with either project.

## License

[MIT](LICENSE)

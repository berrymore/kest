# kest

An Envoy-inspired application server for PHP (and, later, WebAssembly), written in Go.

> **Status: experimental.** Nothing is usable yet: this README describes where the project is going.
> The goal is production quality. The plan is in [`.ai/roadmap.md`](.ai/roadmap.md).

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
- **WASM sandbox (planned).** Run small, isolated WebAssembly modules for hot paths, as handlers or HTTP
  filters, with hard memory and time limits.
- **Built to be operated.** Admin API, metrics, structured logs, graceful shutdown and rolling restarts.

## What it will look like

`kest.yaml`:

```yaml
version: v1

admin:
  address: 127.0.0.1:9901

listeners:
  - name: public
    address: 0.0.0.0:8080
    http:
      filters:                    # run in order; the router always runs last
        - type: request_id
        - type: access_log
      virtual_hosts:
        - name: app
          domains: ["*"]
          routes:
            - match: { prefix: /healthz }
              direct_response: { status: 200, body: "ok" }
            - match: { prefix: /reports/ }
              route: { cluster: php_heavy, timeout: 120s }
            - match: { prefix: / }
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

## Architecture at a glance

```
                ┌──────────────────────────────── kest ─────────────────────────────────┐
                │                                                                       │
  client ─────▶ │  listener ──▶ HTTP filters ──▶ router ──▶ cluster                     │
                │   :8080       request_id,      vhosts,    ├─ php_workers ─▶ pool ─────┼──▶ php worker.php × N
                │               access_log, …    routes     ├─ wasm (planned) ─▶ wazero │
                │                                           └─ http_proxy (planned) ────┼──▶ upstream
                │                                                                       │
                │  admin :9901 — /ready, /config_dump, /clusters, /metrics              │
                └───────────────────────────────────────────────────────────────────────┘
```

Details: [`.ai/architecture.md`](.ai/architecture.md).

## Roadmap

| Milestone | Goal |
|---|---|
| **M0** Foundation | CLI, strict config loader, extension registry, lifecycle, admin `/ready`, `direct_response` |
| **M1** Worker pool | Language-agnostic process pool, supervisor, RoadRunner-compatible relay |
| **M2** PHP HTTP — *MVP* | Serve PSR-7 apps through unmodified RoadRunner HTTP workers |
| **M3** Operability | Metrics, admin API, rolling restarts, access logs, limits, benchmarks |
| **M4** HTTP edge | TLS, HTTP/2, static files, compression, reverse-proxy clusters |
| **M5** WASM sandbox | wazero runtime, `wasm` clusters and filters |
| **M6** Dynamic config | Hot reload of the config file, config API |

## Development

Requirements:

- Go 1.27+
- PHP CLI and Composer, for integration tests and examples (macOS: `brew install php composer`).
  kest itself doesn't restrict the PHP version; the RoadRunner PHP libraries currently need PHP 8.2+,
  and kest is tested against PHP 8.3+.
- Docker, later on, for reproducible end-to-end tests across PHP versions

```sh
go build ./...
go test -race ./...
```

Contributors and AI agents: start with [`AGENTS.md`](AGENTS.md).

## Acknowledgements

kest draws heavily on ideas from RoadRunner (MIT) and Envoy (Apache-2.0). It is not affiliated with either project.

## License

[MIT](LICENSE)

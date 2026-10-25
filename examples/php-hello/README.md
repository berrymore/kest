# php-hello

A PSR-7 application served by kest through a pool of PHP workers. The worker is a plain
[`roadrunner/http`](https://github.com/roadrunner-php/http) loop, the same one RoadRunner runs.

Requirements: PHP 8.2+ with the `sockets` extension, and Composer.

```sh
composer install --working-dir=examples/php-hello
make build
./bin/kest serve -c examples/php-hello/kest.yaml
```

```sh
$ curl 'localhost:8080/?name=kest'
Hello, kest! (PHP 8.5.12, worker 12345)
$ curl -d 'a=1&b[]=2' localhost:8080/info
{"worker_pid":12346,"request_id":"9e1b1e06b987440126f0bba5368e9bbb","booted_at":"…","method":"POST","uri":"http://localhost:8080/info","client":"127.0.0.1","parsed_body":{"a":"1","b":["2"]},"memory":2097152}
$ curl -i localhost:8080/fail       # the exception is logged by kest
HTTP/1.1 500 Internal Server Error
```

Each worker boots once and then serves requests in a loop, so anything set up before the loop is
shared by every request that worker serves. `kest.yaml` explains the pool settings and turns on
three filters: `recovery`, `request_id` (the `X-Request-Id` that `/info` shows) and `access_log` (a log line per
request). The full list is in [`docs/config.md`](../../docs/config.md#php_workers).

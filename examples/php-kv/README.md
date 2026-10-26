# php-kv

PHP workers that keep their state in kest: the [`kv` plugin](../../docs/config.md#kv) holds the data, and the
workers reach it through [RPC](../../docs/config.md#rpc) with
[`roadrunner/kv`](https://github.com/roadrunner-php/kv)'s PSR-16 cache, unchanged, as under RoadRunner. Every worker
of the pool sees the same data, and it outlives worker restarts (not kest's).

Requirements: PHP 8.2+ with the `sockets` extension, and Composer. `roadrunner/kv` talks protobuf: the `protobuf`
extension makes it faster, `google/protobuf` (installed by Composer) works without it.

```sh
composer install --working-dir=examples/php-kv
make build
./bin/kest serve -c examples/php-kv/kest.yaml
```

```sh
$ curl localhost:8080/
visits: 1 (worker 12345)
$ curl localhost:8080/
visits: 2 (worker 12346)
$ curl -X PUT -d 'hello' localhost:8080/notes/a     # kept for an hour
$ curl localhost:8080/notes/a
hello
```

`kest.yaml` turns on the RPC channel (`rpc:`, a socket private to kest and its workers) and the `kv` plugin with one
storage, `local`. The worker connects with
`RPC::create(Environment::fromGlobals()->getRPCAddress())`: kest passes the address in `RR_RPC`.

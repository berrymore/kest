# WebSockets and SSE held by kest — prior art (K-163)

Legend: ✅ verified in the source or docs at the version below (with a link) · ❓ unverified or an estimate.
Checked on 2026-10-26 for M6 (`plans/M6-websockets.md` § D2). Research only: nothing is built. Sizes marked as
estimates are guesses from the code read, not measurements.

## In brief

- **The PHP ecosystems publish over HTTP and authorize in the app.** Laravel (Pusher protocol or Mercure), Symfony
  (Mercure) and RoadRunner's Centrifugo libraries all publish with an HTTP or RPC call that works from any PHP
  process: a request, a queue worker, the CLI. In Pusher and Mercure the hub never calls PHP. The app signs
  (Pusher's HMAC) or issues a JWT (Mercure), and the hub checks it. Only Centrifugo's proxy sends connection events to
  PHP (RoadRunner's `centrifuge` plugin).
- **Laravel now speaks Mercure as well as Pusher.** Laravel 13.32 added a `mercure` broadcaster and Echo 2.6 a
  `mercure` connector, both by Mercure's author. A Mercure hub therefore serves Symfony and Laravel, but it needs
  Mercure 1.0, which is six weeks old, with URL patterns and the subscriptions API.
- **RoadRunner dropped its own `websockets` and `broadcast` plugins in v2.12 (2022)** for Centrifugo, which runs as a
  separate server. RR only bridges Centrifugo's proxy events to a PHP pool and PHP's RPC calls to Centrifugo's API.
- **No WebSocket over HTTP/2 behind nginx.** nginx 1.31 doesn't do RFC 8441 on either side, and Go 1.27 has it off by
  default. SSE needs nothing but `net/http`. A WebSocket needs a library or about 1k lines in house.
- **Several instances.** Reverb, Soketi and Centrifugo fan out through Redis pub/sub. Mercure's open-source hub is
  single-node; its multi-node version is paid. One kest instance is enough for M6; a broker fits behind the hub.

## Versions ✅ (checked 2026-10-26)

| Project | Version | Commit / link |
|---|---|---|
| RoadRunner | v3.0.1 | [`ed89c2e`](https://github.com/roadrunner-server/roadrunner/tree/ed89c2ec991f64b072297fcbee5ad7f058779199) (`container/plugins.go` registers `centrifuge`) |
| `roadrunner-server/centrifuge/v6` | v6.0.0 (in RR v3.0.1's `go.mod`) | [`7fbe4aa`](https://github.com/roadrunner-server/centrifuge/tree/7fbe4aa77594609297bebd4e92dfc88a502f88d3) |
| `roadrunner-server/api-go/v6` (Centrifugo proxy and API protos) | v6.0.0 | [`32b5879`](https://github.com/roadrunner-server/api-go/tree/32b5879680bf26d199c9f3b9faf58e6374a36897) |
| `roadrunner/centrifugo` (PHP; `replace: roadrunner-php/centrifugo`) | 2.5.1 (2026-10-19), MIT | [`e1042e8`](https://github.com/roadrunner-php/centrifugo/tree/e1042e8cc5e29bda681910ee46af5fe7052c2f31) |
| `roadrunner-server/websockets`, `…/broadcast` (archived) | v3.1.0 (2022-11-28) | [`5cf64fe`](https://github.com/roadrunner-server/websockets/tree/5cf64fea90b6d84948a269588cf2a1fad888b254), [`b4f34f7`](https://github.com/roadrunner-server/broadcast/tree/b4f34f7317c2eba54877bdb63a3f341b78f4c03a) |
| Centrifugo | v6.10.2 (2026-10-23) | [`cf21c5d`](https://github.com/centrifugal/centrifugo/tree/cf21c5de6be83a35e02b34e44ad40bdbc5556b9f) |
| `centrifugal/centrifuge` (Go library) · `centrifugal/protocol` | v0.40.1 · v0.23.1 | [`79c893e`](https://github.com/centrifugal/centrifuge/tree/79c893e8692d5caf29e4c1e5983191e6e5ee3530) · [`5c7a02f`](https://github.com/centrifugal/protocol/tree/5c7a02f8bccc137520f66ae274fd3e713aa568de) |
| Centrifugo docs (`centrifugal/centrifugal.dev`) | main, 2026-10-25 | [`2d76e8d`](https://github.com/centrifugal/centrifugal.dev/tree/2d76e8de48c3a580e501d2dc6dcaa33533b6b76d/docs) |
| Mercure hub (`dunglas/mercure`) · spec | v1.1.5 (2026-10-25) · `draft-dunglas-mercure-08` | [`7d01238`](https://github.com/dunglas/mercure/tree/7d012388584b33be9b90b50baeeea6775cce27e8) · [`spec/mercure.md`](https://github.com/dunglas/mercure/blob/7d012388584b33be9b90b50baeeea6775cce27e8/spec/mercure.md) |
| `symfony/mercure` · `symfony/mercure-bundle` | v0.9.0 · v0.6.1 | [`d0dca3c`](https://github.com/symfony/mercure/tree/d0dca3c654858ada61dcecbaee058c72f05de61e) · [`a9f1aff`](https://github.com/symfony/mercure-bundle/tree/a9f1affc1cbd81413938d2035141732b614a9507) |
| FrankenPHP | v1.13.4 (2026-10-26) | [`7f50be9`](https://github.com/php/frankenphp/tree/7f50be9d86261bf7cff9b051ab4fb572e95020ed) |
| `laravel/framework` (Illuminate/Broadcasting) | v13.38.0 (2026-10-20) | [`c42f9ab`](https://github.com/laravel/framework/tree/c42f9abd4a3d2f9a3cd6be109255ddce8092ceb8) |
| `laravel/reverb` · `laravel-echo` | v1.12.0 · v2.6.1 | [`e233471`](https://github.com/laravel/reverb/tree/e233471f53641a447d3b7a2150446d47a0e8c043) · [`e6d5e29`](https://github.com/laravel/echo/tree/e6d5e2969a190a38a4c0e9099a6f1a6985ace9e1) |
| `pusher-js` · `pusher/pusher-php-server` | v8.6.0 · 7.3.0 | [`f53faf9`](https://github.com/pusher/pusher-js/tree/f53faf9f33dbab922a45a919607f4c87ad1b7f72) · [`058d846`](https://github.com/pusher/pusher-http-php/tree/058d8464246118110a341fc2e6e70c7c8b6a7f2c) |
| Soketi | 1.6.1 (2024-03-25, the last release) | [`5d18878`](https://github.com/soketi/soketi/tree/5d188786beaf683aca2115a6247dcdc15c29ac77) |
| Swoole | v6.2.4 (2026-10-25) | [`c7e5145`](https://github.com/swoole/swoole-src/tree/c7e514509451beafd4d70ce5b78943d51e76381c) |
| `github.com/coder/websocket` · `github.com/gorilla/websocket` | v1.8.15 · v1.5.3 | [`9c8faad`](https://github.com/coder/websocket/tree/9c8faadccd1b679e811a79ce506f8a10237251ad) · [`ce903f6`](https://github.com/gorilla/websocket/tree/ce903f6d1d961af3a8602f2842c8b1c3fca58c4d) |
| Go | 1.27.3 | [`2aae100`](https://github.com/golang/go/tree/2aae1000ea1d4b28923b96db28acf4e3b99d6aff) |
| nginx | 1.31.8 (mainline, 2026-10-21) | [`140aa94`](https://github.com/nginx/nginx/tree/140aa942711868a8daeb95810bc9a81f272ada72), [CHANGES](https://nginx.org/en/CHANGES), docs read 2026-10-26 |
| Pushpin | v1.42.0 | [`0e01591`](https://github.com/fastly/pushpin/tree/0e0159101f2e17af9cfb2633e0ac8fceaa4430d3); protocol pages on pushpin.org (unversioned) |

## 1. RoadRunner: `centrifuge`, and the old `websockets` and `broadcast` ✅

Sources: centrifuge [`plugin.go`](https://github.com/roadrunner-server/centrifuge/blob/7fbe4aa77594609297bebd4e92dfc88a502f88d3/plugin.go),
[`proxy.go`](https://github.com/roadrunner-server/centrifuge/blob/7fbe4aa77594609297bebd4e92dfc88a502f88d3/proxy.go),
[`rpc.go`](https://github.com/roadrunner-server/centrifuge/blob/7fbe4aa77594609297bebd4e92dfc88a502f88d3/rpc.go),
[`config.go`](https://github.com/roadrunner-server/centrifuge/blob/7fbe4aa77594609297bebd4e92dfc88a502f88d3/config.go#L14-L46);
api-go [`proxy.pb.go`](https://github.com/roadrunner-server/api-go/blob/32b5879680bf26d199c9f3b9faf58e6374a36897/centrifugo/proxy/v1/proxy.pb.go);
PHP [`README.md`](https://github.com/roadrunner-php/centrifugo/blob/e1042e8cc5e29bda681910ee46af5fe7052c2f31/README.md).

- **Topology.** Centrifugo is a separate server and holds every client connection. RR is a **gRPC server for
  Centrifugo's proxy** (`CentrifugoProxy`, [plugin.go#L117-L127](https://github.com/roadrunner-server/centrifuge/blob/7fbe4aa77594609297bebd4e92dfc88a502f88d3/plugin.go#L117-L127))
  and a **gRPC client of Centrifugo's server API** ([client.go](https://github.com/roadrunner-server/centrifuge/blob/7fbe4aa77594609297bebd4e92dfc88a502f88d3/client.go)).
  Centrifugo points its proxy endpoints at `grpc://<rr>:<port>`.
  - Config: `proxy_address` (default `tcp://127.0.0.1:30000`, or a unix socket), `grpc_api_address` (default
    `127.0.0.1:10000`), `use_compressor`, `tls`, `pool`.
  - The proxy server has no TLS and no authentication ([#L93](https://github.com/roadrunner-server/centrifuge/blob/7fbe4aa77594609297bebd4e92dfc88a502f88d3/plugin.go#L93)).
    The API client has no API key option.
- **A pool of its own** (`centrifuge.pool`, `RR_MODE=centrifuge`, [plugin.go#L109](https://github.com/roadrunner-server/centrifuge/blob/7fbe4aa77594609297bebd4e92dfc88a502f88d3/plugin.go#L109)),
  separate from the HTTP pool.
- **One proxy call is one worker exec** ([proxy.go#L23-L60](https://github.com/roadrunner-server/centrifuge/blob/7fbe4aa77594609297bebd4e92dfc88a502f88d3/proxy.go#L23-L60)).
  - The context is the JSON of the gRPC metadata plus `"type":["connect"]`, the body the protobuf of the proxy
    request, the codec `CODEC_PROTO`.
  - PHP answers with the protobuf of the matching response: a `result`, an `error{code,message,temporary}`, or a
    `disconnect{code,reason}` ([AbstractRequest.php#L52-L90](https://github.com/roadrunner-php/centrifugo/blob/e1042e8cc5e29bda681910ee46af5fe7052c2f31/src/Request/AbstractRequest.php#L52-L90)).
  - Streamed responses are refused.
- **Events:** `connect`, `refresh`, `subscribe`, `publish`, `rpc`, `subrefresh`, and the PRO-only
  `notifycacheempty` and `notifychannelstate`. PHP 2.5.1 maps the PRO-only two to `Invalid`. Subscription streams
  return "not supported" ([proxy.go#L349-L359](https://github.com/roadrunner-server/centrifuge/blob/7fbe4aa77594609297bebd4e92dfc88a502f88d3/proxy.go#L349-L359)).
  - Connect sends `client, transport, protocol, encoding, data, name, version, channels`. The app returns
    `user, expire_at, info, data, channels, subs, meta, caps`.
  - Subscribe sends `user, channel, token, meta, data`. The app returns `expire_at, info, data, override, allow`.
  - Publish sends `user, channel, data`. The app may replace `data` and set `skip_history`.
  - RPC sends `user, method, data`. The app returns `data`.
- **Timeouts.** The exec runs under the gRPC call's context, so Centrifugo's proxy `timeout` bounds it (default 1 s,
  [proxy.md#L127-L142](https://github.com/centrifugal/centrifugal.dev/blob/2d76e8de48c3a580e501d2dc6dcaa33533b6b76d/docs/server/proxy.md#L127-L142)).
- **Publishing outside a request.** PHP calls RR's RPC plugin (`centrifuge.Publish`, `Broadcast`, `Disconnect`,
  `Presence`, `History`, …: 32 methods). RR forwards each to Centrifugo's gRPC API under `context.Background()`, so
  without a timeout ([rpc.go#L81](https://github.com/roadrunner-server/centrifuge/blob/7fbe4aa77594609297bebd4e92dfc88a502f88d3/rpc.go#L81)).
  PHP uses `RPCCentrifugoApi`. Apps can also skip RR and call Centrifugo's HTTP API with `centrifugal/phpcent`, which
  has more Packagist downloads than `roadrunner-php/centrifugo` (2.66M vs 2.43M).
- **The app's code**, paraphrased from the README:
  ```php
  $centrifugo = new CentrifugoWorker($worker = Worker::create(), new RequestFactory($worker));
  while ($request = $centrifugo->waitRequest()) {
      match (true) {
          $request instanceof Request\Connect => $request->respond(new ConnectResponse(user: '1', channels: ['news'])),
          $request instanceof Request\Subscribe => $request->respond(new SubscribeResponse()), // or ->error(403, '…')
          $request instanceof Request\RPC => $request->respond(new RPCResponse(data: ['ok' => true])),
      };
  }
  // anywhere else (an HTTP worker, a job):
  (new RPCCentrifugoApi(RPC::create('tcp://127.0.0.1:6001')))->publish('news', json_encode($msg));
  ```
  The browser uses `centrifuge-js`. Spiral wires this up (`spiral/roadrunner-bridge`'s `CentrifugoBootloader`). No
  Laravel integration goes through RR; third-party Laravel broadcasters call Centrifugo's HTTP API.
- **The old `websockets` plugin** (RR v2, [plugin.go](https://github.com/roadrunner-server/websockets/blob/5cf64fea90b6d84948a269588cf2a1fad888b254/plugin.go)).
  - An HTTP middleware on `/ws` built on `gobwas/ws`, with its own `RR_MODE=http` pool.
  - Authorization went through HTTP-style requests to PHP: one with the attribute `ws:joinServer` before the upgrade,
    one with `ws:joinTopics` on a `{"command":"join","topics":[…]}`. Status 200 allowed; any other status went back
    to the client ([plugin.go#L176-L217](https://github.com/roadrunner-server/websockets/blob/5cf64fea90b6d84948a269588cf2a1fad888b254/plugin.go#L176-L217)).
  - Clients could only `join`, `leave` and set `headers`. Messages came from `broadcast`.
- **The old `broadcast` plugin** was pub/sub over named brokers (`memory`, `redis`). PHP published over RPC
  (`broadcast.Publish`) with `spiral/roadrunner-broadcast`, last released 2022-08, now archived.
- **Removed in RR v2.12.0** (2022-11-24): "`websocket` and `broadcast` plugins were replaced by the new `centrifuge`
  plugin" ([release notes](https://github.com/roadrunner-server/roadrunner/releases/tag/v2.12.0)).
  - Both repositories got a last tag, v3.1.0, on 2022-11-28, and are archived.
  - The reason, from Spiral's docs ([websockets](https://spiral.dev/docs/websockets-configuration), not pinned): the
    plugin "was too limited", so they chose Centrifugo. A fuller reason ("our previous JS client had feeble support")
    showed up only in a search snippet ❓.
- **Fan-out** is Centrifugo's (§ 8). RR keeps no connections and no channel state.

## 2. Centrifugo ✅

Docs at [`2d76e8d`](https://github.com/centrifugal/centrifugal.dev/tree/2d76e8de48c3a580e501d2dc6dcaa33533b6b76d/docs).

- **License.** The server is Apache-2.0 ([LICENSE](https://github.com/centrifugal/centrifugo/blob/cf21c5de6be83a35e02b34e44ad40bdbc5556b9f/LICENSE)).
  The Go library, the protocol and the SDKs (js, go, dart, swift, java) are MIT.
  - PRO is commercial ([pro/overview.md](https://github.com/centrifugal/centrifugal.dev/blob/2d76e8de48c3a580e501d2dc6dcaa33533b6b76d/docs/pro/overview.md)).
    It adds push notifications, rate limits, user status, the connections API, token revocation, channel
    `state`/`cache_empty` hooks, channel patterns, delta compression for at-most-once delivery, tracing and analytics.
- **Client protocol** ([client_protocol.md](https://github.com/centrifugal/centrifugal.dev/blob/2d76e8de48c3a580e501d2dc6dcaa33533b6b76d/docs/transports/client_protocol.md),
  [client.proto](https://github.com/centrifugal/protocol/blob/5c7a02f8bccc137520f66ae274fd3e713aa568de/definitions/client.proto), 847 lines).
  - Commands get replies matched by `id`; pushes have `id` 0.
  - Encodings: JSON (newline-separated batches) or protobuf (varint-length prefixed).
  - 12 commands: connect, subscribe, unsubscribe, publish, presence, presence_stats, history, ping, send, rpc,
    refresh, sub_refresh.
  - 9 pushes: pub, join, leave, unsubscribe, message, subscribe, connect, disconnect, refresh.
  - Pings every 25 s; the pong timeout is 8 s.
- **Transports** ([overview.md](https://github.com/centrifugal/centrifugal.dev/blob/2d76e8de48c3a580e501d2dc6dcaa33533b6b76d/docs/transports/overview.md)).
  - Bidirectional: WebSocket. centrifuge-js also emulates bidirectional connections over HTTP-streaming and SSE:
    client→server goes as a POST to `/emulation`, routed to the node that holds the connection.
  - Unidirectional, with native browser APIs and no SDK: WebSocket, SSE (`/connection/uni_sse?cf_connect={…}`),
    HTTP-streaming, gRPC. Channels are fixed at connect (from the connect proxy or the JWT); there are no dynamic
    subscriptions, no recovery and no RPC.
- **SDKs:** centrifuge-js (browser, Node, React Native), -go, -dart, -swift, -java, -python; -csharp is in progress.
- **Proxy to the backend** ([proxy.md](https://github.com/centrifugal/centrifugal.dev/blob/2d76e8de48c3a580e501d2dc6dcaa33533b6b76d/docs/server/proxy.md)).
  - HTTP POST with JSON, or unary gRPC; both use the same `proxy.proto`.
  - Events: `connect` and `refresh` (per connection), `subscribe`, `publish` and `sub_refresh` (per channel), and
    `rpc`. `subscribe_stream` is experimental; `cache_empty` and `state` are PRO.
  - Only the headers listed in `http_headers` (e.g. `Cookie`) are forwarded. "Headers emulation" lets the SDK send
    headers in its first message.
- **No disconnect or unsubscribe hooks, by design** ([FAQ](https://github.com/centrifugal/centrifugal.dev/blob/2d76e8de48c3a580e501d2dc6dcaa33533b6b76d/docs/faq/index.md#L177-L203)).
  A killed node can't send them, mass reconnects would flood the backend, and they race with reconnects. Use the
  periodic `refresh` instead.
- **JWT instead of the proxy:** a connection token and per-channel subscription tokens (HMAC, RSA, ECDSA, EdDSA,
  JWKS), so reconnect storms don't reach the backend.
- **Server API:** HTTP `POST /api/<method>` with `X-API-Key`, or gRPC on port 10000, which is unauthenticated by
  default ([server_api.md](https://github.com/centrifugal/centrifugal.dev/blob/2d76e8de48c3a580e501d2dc6dcaa33533b6b76d/docs/server/server_api.md)).
- **Open-source core.** Presence with join/leave; history and recovery (stream positions and epochs, replay after a
  reconnect); delta compression on top of history; map subscriptions. The memory engine keeps history and presence in
  one process.
- **Size.** `centrifugal/centrifuge` is about 52.6k lines of non-test Go. Its root package alone is 34k
  (`client.go` 6.4k, `node.go` 2.4k, `hub.go` 1.7k), Redis about 16k, and an internal gorilla/websocket fork 3.1k.
  Centrifugo adds 41.5k.

## 3. Mercure ✅

Spec [`spec/mercure.md`](https://github.com/dunglas/mercure/blob/7d012388584b33be9b90b50baeeea6775cce27e8/spec/mercure.md)
(an individual Internet-Draft, [datatracker](https://datatracker.ietf.org/doc/draft-dunglas-mercure/)).

- **Two protocol versions.** Hub 1.0 (2026-09-16) broke 0.x
  ([UPGRADE.md](https://github.com/dunglas/mercure/blob/7d012388584b33be9b90b50baeeea6775cce27e8/docs/UPGRADE.md)).
  `symfony/mercure` 0.9.0 defaults to 1.0 ([CHANGELOG](https://github.com/symfony/mercure/blob/d0dca3c654858ada61dcecbaee058c72f05de61e/CHANGELOG.md#L7)).
  The hub accepts 0.x clients only when built with deprecation tags and `protocol_version_compatibility 8`.

  | | 0.x | 1.0 |
  |---|---|---|
  | Subscribe | `topic=` (URI template) | `match=` (exact) / `match_urlpattern=` (WHATWG URL Pattern) |
  | Token | `mercure.{publish,subscribe}` claim | OAuth access token: `typ: at+jwt`, `iss`, `aud`, `exp`, `authorization_details` |
  | Cookie / query | `mercureAuthorization` / `?authorization=` | `__Secure-mercure_access_token` / none |
  | Reconnection | `lastEventID`, `Last-Event-ID` | `last_event_id`, `Mercure-Last-Event-ID` |

- **Subscribe:** `GET /.well-known/mercure?match=…` returns `text/event-stream` ([spec#L111-L271](https://github.com/dunglas/mercure/blob/7d012388584b33be9b90b50baeeea6775cce27e8/spec/mercure.md#L111-L271)).
  - Both matcher types are MUST; `*` matches everything.
  - All SSE fields (`id`, `event`, `data`, `retry`) are supported. Keep-alive comments SHOULD be sent; the hub sends
    one every 40 s ([hub.go#L22](https://github.com/dunglas/mercure/blob/7d012388584b33be9b90b50baeeea6775cce27e8/hub.go#L22)).
  - The browser uses the native `EventSource`.
- **Publish:** a form-encoded `POST` to the same URL with a publisher JWT ([spec#L356-L468](https://github.com/dunglas/mercure/blob/7d012388584b33be9b90b50baeeea6775cce27e8/spec/mercure.md#L356-L468)).
  - Fields: `topic` (one or more), `data`, `private`, `id`, `type`, `retry`.
  - The answer is `200` with the update's id.
- **History is optional.** Events MUST carry an `id`, but replay after a reconnect is SHOULD. A hub without history
  answers with `Mercure-Last-Event-ID`, and the client re-fetches state
  ([spec#L921-L1007](https://github.com/dunglas/mercure/blob/7d012388584b33be9b90b50baeeea6775cce27e8/spec/mercure.md#L921-L1007)).
  The open-source `local` transport keeps no history; `bolt` keeps it on disk.
- **Authorization** ([spec#L470-L606](https://github.com/dunglas/mercure/blob/7d012388584b33be9b90b50baeeea6775cce27e8/spec/mercure.md#L470-L606)).
  - Publishers always need a token. Subscribers need one only for private updates.
  - The token comes in `Authorization: Bearer` or a Secure, HttpOnly cookie on the same site.
  - Keys are configured per issuer: HMAC, RSA, EC, EdDSA or JWKS.
  - Errors follow RFC 6750.
- **The PHP app issues the tokens.**
  - `Authorization::setCookie()` or Twig's `mercure()` sets the subscriber cookie
    ([Authorization.php#L55](https://github.com/symfony/mercure/blob/d0dca3c654858ada61dcecbaee058c72f05de61e/src/Authorization.php#L55),
    [MercureExtension.php#L51-L62](https://github.com/symfony/mercure/blob/d0dca3c654858ada61dcecbaee058c72f05de61e/src/Twig/MercureExtension.php#L51-L62)).
  - `Discovery` adds `Link: <hub>; rel="mercure"`.
  - The bundle signs publisher tokens with `jwt.secret` or a JWKS.
- **Active subscriptions** are an optional feature
  ([spec#L1009-L1110](https://github.com/dunglas/mercure/blob/7d012388584b33be9b90b50baeeea6775cce27e8/spec/mercure.md#L1009-L1110)).
  The hub publishes an event per subscribe and unsubscribe on
  `/.well-known/mercure/subscriptions/{match_type}/{match}/{subscriber}`, plus a JSON listing. This is Mercure's
  only presence.
- **Symfony.** `$hub->publish(new Update($topics, $data, private: true))` POSTs with a JWT through `http-client`
  ([Hub.php#L74-L99](https://github.com/symfony/mercure/blob/d0dca3c654858ada61dcecbaee058c72f05de61e/src/Hub.php#L74-L99)).
  - It works from a request, the CLI, or Messenger.
  - The bundle has a `url` and a `public_url` per hub, so pointing them at kest needs no new code.
  - Symfony UX Turbo streams ([bridge](https://github.com/symfony/ux/tree/623c488cd700e8c62f109d924fd33c54c2491001/src/Turbo/src/Bridge/Mercure))
    and API Platform push through Mercure.
- **Laravel.** Laravel 13.32 added a first-party `mercure` broadcaster
  ([PR #61474](https://github.com/laravel/framework/pull/61474), by dunglas, merged 2026-09-10;
  [MercureBroadcaster.php](https://github.com/laravel/framework/blob/c42f9abd4a3d2f9a3cd6be109255ddce8092ceb8/src/Illuminate/Broadcasting/Broadcasters/MercureBroadcaster.php)).
  Echo 2.6 added a `mercure` connector
  ([mercure-connector.ts](https://github.com/laravel/echo/blob/e6d5e2969a190a38a4c0e9099a6f1a6985ace9e1/packages/laravel-echo/src/connector/mercure-connector.ts)).
  - The broadcaster uses `symfony/mercure` 1.0 grants.
  - `/broadcasting/auth` answers with a subscriber cookie granting the channel topics.
  - Presence uses the subscriptions API with a URL pattern (`…/subscriptions/:match_type/<topic>{/:subscriber}?`).
  - Whispers are published by the browser to a per-channel `…/whisper/<channel>` topic under a cookie grant.
  - The older third-party `mvanduijker/laravel-mercure-broadcaster` speaks 0.x and doesn't use Echo.
- **FrankenPHP** embeds the hub as a Go library, which is AGPL; a build tag leaves it out
  ([compile.md#L95](https://github.com/php/frankenphp/blob/7f50be9d86261bf7cff9b051ab4fb572e95020ed/docs/compile.md#L95)).
  - It is configured in the Caddyfile: `mercure { publisher_jwt … subscriber_jwt … }`.
  - PHP can publish in process, without HTTP or a token: `mercure_publish($topics, $data, $private, …)`
    ([mercure.go](https://github.com/php/frankenphp/blob/7f50be9d86261bf7cff9b051ab4fb572e95020ed/mercure.go)),
    which `symfony/mercure`'s `FrankenPhpHub` uses.
- **Licenses.** The hub is **AGPL-3.0** ([LICENSE](https://github.com/dunglas/mercure/blob/7d012388584b33be9b90b50baeeea6775cce27e8/LICENSE)).
  `symfony/mercure`, the bundle and FrankenPHP are MIT.
  - Under ADR-0005 (MIT, BSD, Apache-2.0), kest can't depend on the hub or copy its code; it can implement the spec.
  - The URL Pattern library `dunglas/go-urlpattern` is BSD-3-Clause and pulls in `nlnwa/whatwg-url` and `x/exp`.
- **Size.** The hub's core (subscribe, publish, authorization, matchers, local fan-out; without Bolt, Caddy and
  metrics) is 3,828 lines, 2,394 without blanks and comments. The subscriptions API adds about 1.2k.
- **Browsers** allow 6 SSE connections per origin over HTTP/1.1, across tabs; HTTP/2 multiplexes them
  ([MDN](https://developer.mozilla.org/en-US/docs/Web/API/EventSource)). So the proxy in front should speak h2 to
  browsers.

## 4. The Pusher protocol, as Laravel uses it ✅

Pusher's [WebSocket protocol](https://pusher.com/docs/channels/library_auth_reference/pusher-websockets-protocol/) and
[REST API](https://pusher.com/docs/channels/library_auth_reference/rest-api/), read 2026-10-26; the code links below.

- **Connection.**
  - The client opens `ws[s]://host/app/{key}?protocol=7&client=js&version=8.6.0`
    ([url_schemes.ts#L14-L32](https://github.com/pusher/pusher-js/blob/f53faf9f33dbab922a45a919607f4c87ad1b7f72/src/core/transports/url_schemes.ts#L14-L32)).
  - The server answers `pusher:connection_established` with `{"socket_id":"N.N","activity_timeout":30}`. pusher-js
    requires `activity_timeout`, and pusher-php-server rejects a socket id that isn't `\d+\.\d+`.
  - An unknown key gets `pusher:error` 4001.
- **Envelope.** `{"event","channel"?,"data"}`, where server→client `data` is a JSON **string** inside the JSON
  ([EventHandler.php#L184-L207](https://github.com/laravel/reverb/blob/e233471f53641a447d3b7a2150446d47a0e8c043/src/Protocols/Pusher/EventHandler.php#L184-L207)).
- **Liveness.** The client pings after min(120 s, `activity_timeout`) of silence and waits 30 s for the pong.
  The server answers `pusher:ping` with `pusher:pong` and also takes RFC 6455 pings. Reverb pings connections idle
  past `ping_interval` and closes silent ones with 4201.
- **Subscribe.** `pusher:subscribe {channel, auth?, channel_data?}` →
  `pusher_internal:subscription_succeeded`; `pusher:unsubscribe {channel}`. The type comes from the name's prefix:
  `private-`, `presence-`, `cache-`. Encrypted channels are only relayed; the app encrypts.
- **Error codes:** 4000–4099 don't reconnect (4001 unknown app, 4004 over quota, 4009 unauthorized); 4100–4199
  reconnect with backoff; 4200–4299 reconnect at once (4201 no pong); 4301 client event rate-limited.
- **Private and presence authorization.**
  - pusher-js POSTs `socket_id` and `channel_name` to Echo's `authEndpoint`, `/broadcasting/auth` by default
    ([channel_authorizer.ts#L15-L17](https://github.com/pusher/pusher-js/blob/f53faf9f33dbab922a45a919607f4c87ad1b7f72/src/core/auth/channel_authorizer.ts#L15-L17)).
    That is an ordinary route of the app, decided by `routes/channels.php`
    ([BroadcastManager.php#L81-L95](https://github.com/laravel/framework/blob/c42f9abd4a3d2f9a3cd6be109255ddce8092ceb8/src/Illuminate/Broadcasting/BroadcastManager.php#L81-L95)).
  - The answer is `{"auth":"<key>:<hex HMAC-SHA256(secret, socket_id:channel[:channel_data])>","channel_data"?}`
    ([PusherBroadcaster.php#L104-L129](https://github.com/laravel/framework/blob/c42f9abd4a3d2f9a3cd6be109255ddce8092ceb8/src/Illuminate/Broadcasting/Broadcasters/PusherBroadcaster.php#L104-L129)).
  - The server recomputes the HMAC and **never calls the app** (4009 on a mismatch,
    [InteractsWithPrivateChannels.php#L24-L44](https://github.com/laravel/reverb/blob/e233471f53641a447d3b7a2150446d47a0e8c043/src/Protocols/Pusher/Channels/Concerns/InteractsWithPrivateChannels.php#L24-L44)).
- **Presence.** `channel_data` is `{"user_id","user_info"}`. `subscription_succeeded` carries
  `{"presence":{"count","ids","hash"}}`, deduplicated by `user_id`. `pusher_internal:member_added` and
  `member_removed` fire on a user's first and last connection
  ([InteractsWithPresenceChannels.php](https://github.com/laravel/reverb/blob/e233471f53641a447d3b7a2150446d47a0e8c043/src/Protocols/Pusher/Channels/Concerns/InteractsWithPresenceChannels.php)).
- **Client events (Echo's `whisper`)** are `client-*` events on a private or presence channel. Reverb relays them to
  the channel's other members, never back to the sender. Its `accept_client_events_from` setting is `members` by
  default, `all`, or off ([ClientEvent.php#L15-L82](https://github.com/laravel/reverb/blob/e233471f53641a447d3b7a2150446d47a0e8c043/src/Protocols/Pusher/ClientEvent.php#L15-L82)).
  - **PHP never sees them in Reverb**, which has no webhooks.
  - Soketi sends Pusher webhooks, signed, to the app: `client_event`, `channel_occupied`/`vacated`,
    `member_added`/`removed`.
- **HTTP publish.** `PusherBroadcaster::broadcast()` calls only `trigger()`, in chunks of 100 channels
  ([PusherBroadcaster.php#L158-L175](https://github.com/laravel/framework/blob/c42f9abd4a3d2f9a3cd6be109255ddce8092ceb8/src/Illuminate/Broadcasting/Broadcasters/PusherBroadcaster.php#L158-L175)).
  That is `POST /apps/{id}/events` with a JSON body `{name, data, channels, socket_id?}`, where `socket_id` excludes
  the sender (`toOthers()`).
  - The query carries `auth_key`, `auth_timestamp`, `auth_version=1.0`, `body_md5`, and
    `auth_signature = hex HMAC-SHA256(secret, "POST\n/apps/{id}/events\n" + sorted query)`
    ([Pusher.php#L283-L303](https://github.com/pusher/pusher-http-php/blob/058d8464246118110a341fc2e6e70c7c8b6a7f2c/src/Pusher.php#L283-L303)).
  - Reverb rejects a timestamp more than 600 s off and answers `{}`.
  - Laravel never calls `batch_events`, the channel queries, or `terminate_connections`.
- **Configuration is environment only.** The `reverb` driver is the `pusher` driver with a host, port and scheme
  ([BroadcastManager.php#L345-L392](https://github.com/laravel/framework/blob/c42f9abd4a3d2f9a3cd6be109255ddce8092ceb8/src/Illuminate/Broadcasting/BroadcastManager.php#L345-L392)).
  The default `config/broadcasting.php` reads `REVERB_APP_ID/KEY/SECRET` and `REVERB_HOST/PORT/SCHEME`. Echo's
  `broadcaster: 'reverb'` is pusher-js with `wsHost`, `wsPort` and `enabledTransports: ['ws','wss']`.
  `ShouldBroadcast` events publish from a queue worker.
- **Reverb** is PHP on ReactPHP with `ratchet/rfc6455`, one event-loop process (`reverb:start`), MIT
  ([composer.json](https://github.com/laravel/reverb/blob/e233471f53641a447d3b7a2150446d47a0e8c043/composer.json#L21-L36)).
  Defaults ([config/reverb.php](https://github.com/laravel/reverb/blob/e233471f53641a447d3b7a2150446d47a0e8c043/config/reverb.php#L39-L96)):
  messages and API requests ≤ 10,000 bytes, no connection limit, `ping_interval` 60 s, `activity_timeout` 30 s,
  origins `*`.
- **Soketi** is Node.js on uWebSockets.js, AGPL-3.0. Its last release and last commit are from 2024-03, and its
  README calls maintenance "infrequent". It does protocol 7 in about 1.6k lines of TypeScript.
- **The minimal subset for a stock Laravel app with Echo** (public, private, presence channels):
  - the upgrade on `/app/{key}`, `connection_established`, ping and pong, idle closing;
  - `subscribe` and `unsubscribe` with the HMAC check;
  - the presence member table with `member_added`/`member_removed`;
  - unsubscribe-all on close;
  - `POST /apps/{id}/events` with the signature check and `socket_id` exclusion;
  - optionally, relaying whispers.

  Not needed: `batch_events`, the GET endpoints, `pusher:signin`, cache channels, webhooks, SockJS and XHR fallbacks.
  Reverb's `src/Protocols/Pusher` is 1,723 lines of PHP without comments. An estimate for Go on top of a WebSocket
  library: 700–1,000 lines plus tests ❓.

## 5. Swoole / OpenSwoole, for contrast ✅

Sources: Swoole [`examples/websocket/server.php`](https://github.com/swoole/swoole-src/blob/c7e514509451beafd4d70ce5b78943d51e76381c/examples/websocket/server.php),
[`php_swoole_websocket.stub.php`](https://github.com/swoole/swoole-src/blob/c7e514509451beafd4d70ce5b78943d51e76381c/ext-src/stubs/php_swoole_websocket.stub.php);
OpenSwoole docs: [WebSocket server](https://openswoole.com/docs/modules/swoole-websocket-server),
[configuration](https://openswoole.com/docs/modules/swoole-server/configuration).

- **The PHP process is the server**, a C extension. The app registers callbacks: `handshake` (optional),
  `open(Server, Request)`, `message(Server, Frame)` with `$frame->fd` and `->data`, and `close(Server, $fd)`.
  Plain HTTP goes to `request` on the same port.
- **Sending is by connection id:** `push($fd, $data, $opcode, $flags)`, `isEstablished($fd)`,
  `disconnect($fd, $code, $reason)`, `ping($fd)`.
  - There are no channels. A broadcast is a loop over the fds the app tracks itself, in a `Swoole\Table` or in Redis,
    since workers are separate processes.
  - `dispatch_mode` 2 (by fd) is the default, so every frame of a connection reaches the same worker.
  - ❓ Whether a reload of the workers keeps the connections was not checked.
- **Contrast with kest.** In Swoole the app's own long-lived process owns every connection, and the app writes the
  server: callbacks, fd bookkeeping, fan-out across workers and hosts. kest's workers keep no state between
  requests, so kest must hold the connections and subscriptions, and PHP sees events, not sockets.

## 6. Go WebSocket libraries, and WebSockets over HTTP/2

| | `github.com/coder/websocket` | `github.com/gorilla/websocket` |
|---|---|---|
| Version checked | v1.8.15 (2026-06-15) | v1.5.3 (2024-06-14) |
| Activity ✅ | commits up to 2026-10-24 (a Go 1.26 update, performance work); formerly `nhooyr.io/websocket` | last commit 2025-03-19; archived in 2022, then revived by new maintainers ❓ (the archive dates weren't checked) |
| License ✅ | **ISC** (permissive, but not in ADR-0005's list) | BSD-2-Clause |
| Dependencies ✅ | none (`go.mod` has no `require`) | none |
| `context.Context` ✅ | `Read(ctx)`, `Write(ctx, …)`, `Ping(ctx)`; `Close(code, reason)`, `CloseNow()` | none: `Set{Read,Write}Deadline`, `ReadMessage`, `WriteMessage` |
| Concurrency ✅ | everything may run concurrently except `Reader`/`Read` ([conn.go#L30](https://github.com/coder/websocket/blob/9c8faadccd1b679e811a79ce506f8a10237251ad/conn.go#L30)) | one reader and one writer at a time ([doc.go#L122-L134](https://github.com/gorilla/websocket/blob/ce903f6d1d961af3a8602f2842c8b1c3fca58c4d/doc.go#L122-L134)) |
| Read limit ✅ | 32 KiB per message by default ([read.go#L88-L107](https://github.com/coder/websocket/blob/9c8faadccd1b679e811a79ce506f8a10237251ad/read.go#L88-L107)) | none unless `SetReadLimit` |
| permessage-deflate ✅ | off by default; with or without context takeover ([compress.go#L17-L50](https://github.com/coder/websocket/blob/9c8faadccd1b679e811a79ce506f8a10237251ad/compress.go#L17-L50)) | "EXPERIMENTAL", no context takeover ([doc.go#L201-L225](https://github.com/gorilla/websocket/blob/ce903f6d1d961af3a8602f2842c8b1c3fca58c4d/doc.go#L201-L225)) |
| Wrapped `ResponseWriter` ✅ | follows `Unwrap()` to the `Hijacker` ([hijack.go](https://github.com/coder/websocket/blob/9c8faadccd1b679e811a79ce506f8a10237251ad/hijack.go)) | asserts `http.Hijacker` on the writer itself ([server.go#L175](https://github.com/gorilla/websocket/blob/ce903f6d1d961af3a8602f2842c8b1c3fca58c4d/server.go#L175)) |
| Origin ✅ | same origin by default; `OriginPatterns` | same origin when `CheckOrigin` is nil ([server.go#L61-L68](https://github.com/gorilla/websocket/blob/ce903f6d1d961af3a8602f2842c8b1c3fca58c4d/server.go#L61-L68)) |
| Non-test code ✅ | 4,260 lines with the dialer and the WASM client; the server path is about 2,500 | 3,577 lines with the client |

- ✅ `golang.org/x/net/websocket` itself points to these two as "more actively maintained"
  ([websocket.go](https://github.com/golang/net/blob/c1d31df183e51b1075a2fd670d6f6f4efb871773/websocket/websocket.go), x/net v0.61.0).
- ✅ Both accept only an HTTP/1.1 `GET` with `Upgrade`, through `Hijack`
  ([coder accept.go#L184-L205](https://github.com/coder/websocket/blob/9c8faadccd1b679e811a79ce506f8a10237251ad/accept.go#L184-L205)).
  coder/websocket's issue [#4, "WebSockets over HTTP/2"](https://github.com/coder/websocket/issues/4) is open.
- ✅ kest's filters already expose `Unwrap()` on their writers (`compression`, `sendfile`, `httpx/hook.go`), and
  coder/websocket and `http.ResponseController` follow it.
- **In house** ❓ (an estimate). A server-only RFC 6455 without compression is roughly 800–1,200 lines plus tests:
  - the handshake;
  - the frame codec with masking;
  - fragmentation, and control frames between fragments;
  - the close handshake;
  - UTF-8 checks on text frames;
  - limits.

  The Autobahn test suite (in Docker) is the usual conformance check. Bugs live in the close handshake, in
  interleaved control frames and in compression.
- ✅ **net/http and long-lived connections** (Go 1.27.3,
  [`server.go#L3213-L3268`](https://github.com/golang/go/blob/2aae1000ea1d4b28923b96db28acf4e3b99d6aff/src/net/http/server.go#L3213-L3268)):
  - `Close` and `Shutdown` neither close nor wait for hijacked connections. Their deadlines are whatever the server
    set, such as `write_timeout`, and are the caller's to clear.
  - An SSE stream is not hijacked, so `Shutdown` waits for its handler: the handler must watch a stop signal.
    `write_timeout` would cut the stream unless each write resets it with `http.ResponseController.SetWriteDeadline`.

**WebSockets over HTTP/2 (RFC 8441)** ✅

- **Go 1.27.3 implements extended CONNECT on the server, but off by default.** It advertises
  `SETTINGS_ENABLE_CONNECT_PROTOCOL` only with `GODEBUG=http2xconnect=1`, because browsers would then try WebSockets
  over HTTP/2 and break libraries that need `Hijack`
  ([internal/http2/http2.go#L37-L61](https://github.com/golang/go/blob/2aae1000ea1d4b28923b96db28acf4e3b99d6aff/src/net/http/internal/http2/http2.go#L37-L61),
  [server.go#L776-L778](https://github.com/golang/go/blob/2aae1000ea1d4b28923b96db28acf4e3b99d6aff/src/net/http/internal/http2/server.go#L776-L778);
  [#71128](https://github.com/golang/go/issues/71128)).
  - With it on, a handler sees `CONNECT` with a `:protocol` header
    ([#L2127-L2161](https://github.com/golang/go/blob/2aae1000ea1d4b28923b96db28acf4e3b99d6aff/src/net/http/internal/http2/server.go#L2127-L2161)).
  - Neither library above handles that. Centrifuge's internal gorilla fork does, under the same GODEBUG
    ([server.go#L197-L237](https://github.com/centrifugal/centrifuge/blob/79c893e8692d5caf29e4c1e5983191e6e5ee3530/internal/websocket/server.go#L197-L237)),
    with an HAProxy example.
  - The Go proposal for WebSockets over HTTP/2, [#49918](https://github.com/golang/go/issues/49918), is open.
- **nginx 1.31.8 doesn't do RFC 8441 on either side.** It has proxied to HTTP/2 upstreams since 1.29.4
  (`proxy_http_version 2`). But neither its HTTP/2 server (`src/http/v2/ngx_http_v2.c`) nor
  `ngx_http_proxy_v2_module.c` contains `:protocol` or `ENABLE_CONNECT_PROTOCOL` (searched at
  [`140aa94`](https://github.com/nginx/nginx/tree/140aa942711868a8daeb95810bc9a81f272ada72/src/http)). Browsers then
  open a separate HTTP/1.1 connection for a WebSocket, and nginx proxies it over HTTP/1.1.
- So behind nginx, RFC 8441 gains kest nothing: WebSockets need HTTP/1.1 between nginx and kest.

## 7. Behind nginx ✅

Sources: [WebSocket proxying](https://nginx.org/en/docs/http/websocket.html),
[`ngx_http_proxy_module`](https://nginx.org/en/docs/http/ngx_http_proxy_module.html), [CHANGES](https://nginx.org/en/CHANGES).

- **WebSocket.**
  - `Upgrade` and `Connection` are hop-by-hop, so the location must pass them:
    `proxy_set_header Upgrade $http_upgrade; proxy_set_header Connection $connection_upgrade;`, with the documented
    `map $http_upgrade $connection_upgrade { default upgrade; '' close; }`.
  - Since 1.29.7, HTTP/1.1 to the upstream is the default and no `Connection` header is sent by default. Before it,
    `proxy_http_version 1.1` was needed.
  - nginx tunnels the connection after a `101`.
- **Idle.** nginx closes a proxied WebSocket after `proxy_read_timeout` (60 s by default) without data from the
  upstream. The docs suggest raising it **or having the server send pings**, so kest's default ping interval should
  be under 60 s.
- **SSE.** `proxy_buffering` is on by default. The upstream can turn it off per response with `X-Accel-Buffering: no`,
  unless `proxy_ignore_headers` drops the header. If kest sets it on SSE responses, nginx needs no special
  configuration. The same `proxy_read_timeout` applies, so a hub sends a comment line (`:\n\n`) more often than that.
- **Reload.** `worker_shutdown_timeout` bounds how long the old nginx workers keep proxied WebSockets after a reload
  (fixed for them in 1.13.7). On its own stop, kest closes its side (`1001 Going Away`; the end of an SSE stream).
- **Unix sockets** (K-164): `proxy_pass http://unix:/…;` carries WebSocket tunnels too; a `tcp_nodelay` bug for this
  case was fixed in 1.3.16.

## 8. Several server instances ✅

- **Centrifugo** ([engines.md](https://github.com/centrifugal/centrifugal.dev/blob/2d76e8de48c3a580e501d2dc6dcaa33533b6b76d/docs/server/engines.md)).
  An engine is a broker plus a presence manager. A message published on any node reaches the subscribers on all.
  - `memory`: one node only.
  - `redis`: pub/sub between nodes, history in Streams, and presence. Sentinel, sharding and Cluster are supported,
    and so are Valkey, KeyDB and Dragonfly.
  - `nats`: at-most-once pub/sub, with no history or recovery; presence needs Redis.
  - `postgres` (experimental): an outbox with history.
- **Reverb.** With `REVERB_SCALING_ENABLED`, every dispatch is PUBLISHed to one Redis channel instead of being
  delivered locally: API publishes, whispers, member events
  ([EventDispatcher.php#L18-L41](https://github.com/laravel/reverb/blob/e233471f53641a447d3b7a2150446d47a0e8c043/src/Protocols/Pusher/EventDispatcher.php#L18-L41)).
  - Every node subscribes and delivers to its own connections.
  - There is no shared store. Presence and channel queries are a scatter-gather over the same pub/sub, with a 10 s
    timeout ([MetricsHandler.php#L41-L52](https://github.com/laravel/reverb/blob/e233471f53641a447d3b7a2150446d47a0e8c043/src/Protocols/Pusher/MetricsHandler.php#L41-L52)).
  - Any node takes the HTTP API.
- **Soketi** has the same design with `local`, `cluster`, `redis` and `nats` adapters
  ([redis-adapter.ts#L30-L80](https://github.com/soketi/soketi/blob/5d188786beaf683aca2115a6247dcdc15c29ac77/src/adapters/redis-adapter.ts#L30-L80)).
- **Mercure.** The open-source hub is single-node (`local`, `bolt`); nodes behind a balancer don't see each other's
  updates. Multi-node is the paid hub, with shared transports on Redis, PostgreSQL, Kafka or Pulsar
  ([high-availability.md](https://github.com/dunglas/mercure/blob/7d012388584b33be9b90b50baeeea6775cce27e8/docs/production/high-availability.md#L21-L45)).
- **RoadRunner** delegates it to Centrifugo. Its old `broadcast` plugin had a `redis` broker.
- **For kest:** every design above is broadcast-everything pub/sub between nodes, plus scatter-gather or a shared
  store for presence and history. A single instance needs neither, and a broker fits behind the hub's dispatch without
  changing the client protocol. A Redis client is a new dependency; Pub/Sub over RESP is small enough to write ❓.

## Also checked: Pushpin's GRIP and WebSocket-over-HTTP ✅

This is not on D2's list, but it is the existing wire format for "a connection's events go to the backend as short
HTTP requests". Pushpin is by Fastly, Apache-2.0
([v1.42.0](https://github.com/fastly/pushpin/tree/0e0159101f2e17af9cfb2633e0ac8fceaa4430d3)). Protocol pages:
[WebSocket-over-HTTP](https://pushpin.org/docs/protocols/websocket-over-http/) and
[GRIP](https://pushpin.org/docs/protocols/grip/).

- **SSE and HTTP streaming.** The client's request reaches the backend as an ordinary request. The backend authorizes
  it and answers with `Grip-Hold: stream` and one or more `Grip-Channel: <name>` headers; `Grip-Keep-Alive` is
  optional, with a 55 s default. The proxy keeps the response open, and the backend's worker is free.
- **WebSocket-over-HTTP.** The proxy POSTs a connection's events to the backend as `application/websocket-events`:
  `OPEN`, `TEXT <hex length>`, `BINARY`, `PING`, `PONG`, `CLOSE`, `DISCONNECT`.
  - Events are batched, with one request in flight per connection.
  - The handshake's headers are replayed on every request. `Connection-Id` identifies the connection, and
    `Set-Meta-*` → `Meta-*` keeps per-connection state.
  - The response body carries events back. A `c:{"type":"subscribe","channel":"…"}` message subscribes the
    connection.
  - `Keep-Alive-Interval` makes the proxy send an empty request when the connection is idle.
- **Publishing:** `POST /publish/` on a control port, with
  `{"items":[{"channel":"…","formats":{"ws-message":{"content":"…"}}}]}`.
- **No PHP users.** The PHP library `fanout/grip` (MIT) was last released in 2023-12, and the older
  `fanout/php-gripcontrol` is archived. GRIP is a worked-out wire format to borrow, not an ecosystem.

## Options for kest

All of them share a core:

- **Connections.** kest holds the WebSocket or SSE connections and a hub of channel → subscribers.
- **Bounds.** A bounded send queue per connection, with a policy for a slow reader; limits on connections,
  subscriptions per connection and message size; ping and idle timers under nginx's 60 s.
- **Lifecycle.** Draining on stop (`1001`, ending SSE streams), since `Shutdown` doesn't, and metrics.
- **Scope.** One instance in M6, with a broker interface left for later.

Estimate for this core: 600–1,000 lines plus tests ❓. The options differ in the protocol on the wire, in whether PHP
sees events, and in how PHP publishes. They can be combined: a second protocol is an adapter over the same hub.

### A. A Pusher-compatible hub (Laravel Broadcasting and Echo)

- **What PHP apps gain.** Stock Laravel with `BROADCAST_CONNECTION=reverb` or `pusher`, and Echo with pusher-js, work
  with env changes only.
  - Public, private and presence channels, `toOthers()`, model broadcasting, and the React and Vue hooks all work.
  - `/broadcasting/auth` is an ordinary route that kest already serves from the pool.
  - Publishing works from anywhere (a request, a queue worker, the CLI) through `pusher-php-server`'s signed HTTP
    POST, so it doesn't need M7.
  - pusher-js and pusher-php-server work with any frontend or PHP framework, but there is no Symfony integration ❓.
- **What kest builds.**
  - A WebSocket transport: a library or in house.
  - The protocol subset of § 4, about 700–1,000 lines ❓.
  - `POST /apps/{id}/events` with HMAC verification, on a route or on the admin listener.
  - An app id, key and secret in the config, shared with the app's env.
  - No new PHP code, no PHP event plumbing, and no dependency beyond the WebSocket library.
- **Risks and limits.**
  - The protocol is a vendor's de-facto spec, with quirks: double-encoded `data`, the socket id's format, close codes.
  - Reverb is the reference implementation, and it is free. kest's gain over running `reverb:start` next to kest is
    operational: one process and one listener fewer, Go per-connection costs, kest's limits and metrics. It is not
    a new capability.
  - PHP authorizes by signing and is never called on connect, subscribe or a client message. Whispers go peer to
    peer. A `client_event` call to a pool, as in Soketi's webhooks, would be an extension.
  - Laravel only. WebSocket only, since Echo sets `enabledTransports: ['ws','wss']`.

### B. A Mercure-compatible SSE hub (Symfony, and now Laravel)

- **What PHP apps gain.**
  - Symfony: `symfony/mercure` and the bundle, Twig's `mercure()`, UX Turbo streams and API Platform work by setting
    `MERCURE_URL`/`MERCURE_PUBLIC_URL` to kest.
  - Laravel 13.32+: the `mercure` broadcaster with Echo's `mercure` connector.
  - The browser needs only `EventSource`, with no SDK and no WebSocket.
  - Publishing works from anywhere through the JWT-signed POST, so it doesn't need M7. A FrankenPHP-style in-process
    `mercure_publish()` would.
- **What kest builds.**
  - SSE on `net/http` (no WebSocket library).
  - The POST endpoint.
  - The 1.0 JWT check: `at+jwt`, `iss`/`aud`/`exp`, `authorization_details`, keys per issuer. HS256 is small on the
    standard library, and RS256/ES256 are standard library too; a JWT library is optional.
  - Exact and `*` matchers, private updates, heartbeats, CORS with credentials, and `Mercure-Last-Event-ID`.
  - For Laravel presence and conformance: `match_urlpattern` (a subset, or `dunglas/go-urlpattern` and its
    dependencies), the subscriptions API, and browser-published whispers.
  - Optional: a history ring buffer, and 0.x compatibility for older apps.
  - Roughly 800–1,500 lines without URL patterns or the subscriptions API ❓.
- **Risks and limits.**
  - Mercure 1.0 is six weeks old, and the 1.1.x patches still adjust compatibility.
  - The spec is an individual I-D with many MUSTs, and kest would have to choose 1.0 only or both versions.
  - The reference hub is AGPL, so kest can only reimplement it.
  - Traffic is server→client only: clients send by POSTing to the app, or to the hub under a grant.
  - PHP is never called by the hub.
  - Over HTTP/1.1, browsers cap SSE at 6 connections per origin, so nginx should speak h2 to browsers.

### C. Centrifugo-style: connection events proxied to a PHP pool (RoadRunner parity)

- **What PHP apps gain.**
  - The most PHP-centric model, and M6's goal as worded: PHP decides `connect`, `subscribe` and `publish`, answers
    `rpc`, and returns the user and channels.
  - If kest speaks Centrifugo's client protocol and RR's centrifuge payload (`RR_MODE=centrifuge`, protobuf proxy
    messages), `roadrunner/centrifugo` workers (Spiral) and `centrifuge-js` and its SDKs work unchanged.
  - Publishing from PHP goes through RR's `centrifuge.*` RPC methods (M7 brought forward), or an emulation of
    Centrifugo's `POST /api/publish` for `phpcent`.
- **What kest builds.**
  - A WebSocket transport.
  - A subset of Centrifugo's client protocol in JSON, and probably protobuf: 12 commands and 9 pushes. The SDKs
    expect positions and recovery ❓ (whether they tolerate their absence wasn't checked).
  - The proxy events as worker execs, which kest's pools, lanes and timeouts already provide.
  - The protobuf messages, which can be hand-encoded as `internal/rrhttp/proto.go` already does, so no new dependency.
  - Much larger than A or B: Centrifugo's own core is 34k lines, and a usable subset is several thousand ❓.
- **Risks and limits.**
  - The largest protocol surface, and it moves: Centrifugo 6.x releases often.
  - The users it serves (RR with Centrifugo, mostly Spiral) already run Centrifugo.
  - Keeping Centrifugo external and having kest play RR's plugin role (a gRPC proxy server and an API client) needs
    `google.golang.org/grpc`, and it leaves the connections outside kest, contrary to M6's goal.
  - A burst of reconnects becomes a burst of PHP execs. Centrifugo offers JWTs for that reason.

### D. kest's own small protocol: events to PHP as ordinary HTTP requests (GRIP-like)

- **What PHP apps gain.**
  - **SSE:** the browser's `GET` reaches PHP as an ordinary request on the app's own route, with its sessions,
    cookies and middleware. PHP answers with a hold response (`Grip-Hold: stream`, `Grip-Channel: …`, or kest's
    equivalent), and kest keeps the stream while the worker is free. `EventSource` is the client.
  - **WebSocket:** the upgrade request goes to PHP the same way (allow with channels and metadata, or deny). Each
    client message becomes a short POST to a route, with a connection id and metadata, routed and laned like any
    request. The response carries messages back and subscribe or close commands.
  - Any framework can use it, since it is plain PSR-7. A thin PHP helper is optional, and the JS side is the plain
    WebSocket API.
  - Publishing goes through a small HTTP endpoint (GRIP's `/publish/` shape, or kest's own), and later through M7's
    RPC.
- **What kest builds.**
  - The core, request synthesis into the existing routing and pools, one request in flight per connection, and the
    publish endpoint.
  - About 1,000–1,500 lines with a WebSocket library ❓. The SSE half needs no library.
- **Risks and limits.**
  - No existing client or PHP library uses it; GRIP's PHP library is stale. Every app writes new code on both sides,
    which is the cost the user wants to avoid.
  - kest owns the contract: documentation and versioning.
  - Chatty clients turn into PHP requests, so this needs per-connection rate limits and a lane.
  - The SSE half alone (hold plus channels) is the cheapest way to give "PHP decides who may subscribe" to any app.

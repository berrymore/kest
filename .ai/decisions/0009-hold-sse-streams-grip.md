# ADR-0009: Hold server-sent event streams in kest, decided by PHP through GRIP

- **Status:** Proposed; on hold since 2026-10-26 (WebSockets and SSE shelved until an application needs them)
- **Date:** 2026-10-26
- **Deciders:** user, agent

## Context

M6 was about the connections PHP can't hold (`plans/M6-unix-sockets.md` § D2, where the feature was shelved). The goal is that a browser keeps a live
connection to kest while no PHP worker is busy for it, PHP decides who may connect and subscribe, and a message PHP
publishes reaches every subscriber. Today an SSE endpoint in PHP works through streamed responses (K-088), but it
ties up one worker per open stream, so a pool of 16 workers serves 16 listeners.

The research (`research/websockets.md`, K-163) found:

- **The PHP ecosystems already have hubs, and none of them calls PHP.**
  - Laravel uses the Pusher protocol (Reverb or Pusher), and since 13.32 Mercure too.
  - Symfony uses Mercure.
  - RoadRunner gave up its own WebSocket plugins in 2022 for Centrifugo, which runs as a separate server.
  - In Pusher and Mercure, the app signs (Pusher's HMAC) or issues a JWT (Mercure) ahead of time, and publishes with a
    signed HTTP POST from any process.
  - Only Centrifugo's proxy sends connection events to PHP, and it is by far the largest protocol.
- **A built-in Pusher or Mercure hub gains operations, not capability.** It saves a process (`reverb:start`, the
  Mercure hub) and puts kest's limits and metrics on the connections. Its cost:
  - Pusher: a WebSocket library and about 700–1,000 lines of protocol on top of a common hub core (600–1,000 lines);
  - Mercure 1.0: six weeks old, with JWT access tokens, URL patterns and a subscriptions API for Laravel's presence;
    its reference hub is AGPL, so kest could only reimplement it.
- **SSE needs nothing beyond `net/http`.** A WebSocket needs a library or about 1k lines of RFC 6455 in house.
  coder/websocket is maintained but ISC-licensed (outside ADR-0005's list); gorilla/websocket is stale. Behind nginx,
  WebSockets go over HTTP/1.1 whatever the browser speaks.
- **GRIP** (Pushpin's protocol, run as a service by Fastly Fanout) is a worked-out wire format for exactly this:
  - the backend answers the client's request with `Grip-Hold: stream` and `Grip-Channel` headers;
  - the proxy keeps the response open and frees the backend;
  - publishers `POST` items per channel to a publish endpoint.

  It has no PHP following: its PHP library is stale.

Constraints: the gain must pay for the complexity (the user cut prefork and socket relays for that reason), stdlib
first (ADR-0005), and kest stays behind a proxy (ADR-0008).

## Decision

We will hold SSE streams in kest, decided by PHP through a subset of GRIP, and leave WebSockets out of M6.

1. **Subscribing is an ordinary request to PHP.**
   - The browser's `EventSource` request goes through kest's routing to the app's own route, with its session,
     cookies and middleware, so PHP decides who may subscribe the way it decides anything else.
   - To subscribe, PHP answers with `Grip-Hold: stream`, one or more `Grip-Channel: <name>` headers and, if it
     likes, a first body: headers, the current state, missed events.
   - kest strips the `Grip-*` headers, sends the response, adds `X-Accel-Buffering: no` for nginx, and keeps the
     stream open on those channels. The worker is free as soon as PHP has answered.
   - An answer without `Grip-Hold` is an ordinary response.
2. **Publishing is an authenticated `POST` to kest** in GRIP's publish format: items with a channel and an
   `http-stream` content, which PHP formats as SSE events.
   - It works from any PHP process: a request, a queue worker, the CLI. It needs no M7 RPC and no PHP package; a
     helper can come later.
   - kest appends each item's content to every stream on its channel.
3. **One hub per kest, in memory, bounded:**
   - limits on open streams, channels per stream and the size of a published item;
   - a send buffer per stream: a reader that falls behind is disconnected, and its `EventSource` reconnects;
   - a keep-alive comment well under nginx's 60 s `proxy_read_timeout`;
   - draining on stop, which ends the streams so that clients reconnect elsewhere;
   - metrics on streams, channels, published and dropped items.
4. **No history in kest.** A reconnect is a new request to PHP with `Last-Event-ID`, so the app can replay what was
   missed in the first body. Several kest instances (a broker behind the hub) come later.
5. **WebSockets are not in M6.** SSE down plus ordinary POSTs to PHP up covers notifications, live updates, chat and
   dashboards. WebSockets, and the stock clients Echo and Turbo, come later as adapters over the same hub when an app
   needs them:
   - GRIP's WebSocket-over-HTTP, where a connection's events reach PHP as requests;
   - a Pusher adapter for Laravel Echo;
   - a Mercure adapter for Symfony and Laravel.
6. **Only the GRIP subset kest implements is promised,** documented in `docs/`; the rest of GRIP is refused or
   ignored, explicitly. The exact subset (headers, keep-alive, the publish format, authentication) is checked against
   Pushpin's docs and source in the plan.

## Alternatives considered

- **A. A Pusher-compatible hub.** Stock Laravel and Echo work with env changes. But it is Laravel only and WebSocket
  only, needs a library (ISC) or RFC 6455 in house, and its gain over running Reverb is operational. It is a good
  second adapter if Laravel users ask for one.
- **B. A Mercure-compatible SSE hub.** It brings Symfony's stock integrations (the bundle, Turbo streams, API
  Platform) and Laravel 13.32+. But it chases a six-week-old 1.0 with many MUSTs: OAuth-style JWTs, URL patterns, a
  subscriptions API. Its gain over the Mercure hub, a small binary, is again operational. It is the natural first
  adapter over this hub if those integrations are wanted.
- **C. Centrifugo-style events to a PHP pool.** It is the most PHP-centric model and gives parity with
  `roadrunner/centrifugo`. But it is the largest surface by far (several thousand lines), needs M7's RPC to publish,
  and serves users who already run Centrifugo.
- **D, in full: GRIP with WebSockets now.** It has the same model for both transports, but it adds a library or RFC
  6455 in house, and a request to PHP per client message, all for needs that SSE plus POST already cover. It can
  come next on the same hub.
- **E. Build nothing; document Reverb or the Mercure hub next to kest, behind nginx.** It costs nothing and solves the
  problem today. It stays the advice for apps that want Echo or Turbo before an adapter exists. But kest would hold no
  connection, and M6's goal and the user's priority would go unmet.
- **kest's own header names instead of GRIP's.** They cost the same, but GRIP is documented, keeps apps portable to
  Pushpin and Fastly Fanout, and spares kest a contract of its own.

## Consequences

- Positive:
  - **Small.** A hub core and a filter, about 1,000 lines with tests, and no new dependency.
  - **No lock-in.** Any framework can use it, since it is plain PSR-7, and the browser's own `EventSource` is the
    client.
  - **PHP keeps control.** Authorization and replay stay in the app, on its routes, under kest's lanes and timeouts.
  - **Portable.** An app is portable to Pushpin and Fastly Fanout.
- Negative / costs:
  - **New app code.** An app writes a few lines: two headers on its subscribe route, and a POST to publish. Echo,
    Turbo and API Platform don't work with it until an adapter exists.
  - **One direction.** Server-to-client only; client-to-server stays ordinary requests.
  - **One instance.** Streams are on one instance; behind a load balancer, a publisher must reach every instance or
    wait for a broker.
  - **HTTP/2 to browsers.** Browsers cap SSE at six connections per origin over HTTP/1.1, so the proxy in front
    should speak HTTP/2 to browsers (operations guide).
- Learned before the feature was shelved (the plan's D2): several instances, including the two colors of a
  blue-green deploy, need a broker (Redis pub/sub) from the start, not later; a deploy closes every held stream, so
  kest needs a staggered drain, `retry:` with jitter and a lane for the reconnect burst; replay comes from the app
  (`Last-Event-ID`) or from the broker's history. A hub outside the app's containers keeps connections through app
  deploys. Revise points 3 and 4 above when this is picked up.
- Follow-ups:
  - backlog items for the hub and the hold filter, the publish endpoint, the GRIP subset and the operations guide;
  - icebox entries for WebSockets (GRIP WebSocket-over-HTTP or Pusher), a Mercure adapter, and a broker for several
    instances;
  - `architecture.md` and `context.md` (the glossary: hub, channel) once implemented.

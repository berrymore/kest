# Plan: M6 — WebSockets, SSE and unix sockets

- **Status:** Active (approved by the user on 2026-10-26, D1's schema included). The unix socket listeners are
  designed (D1). WebSockets and SSE wait for research and an ADR (K-163, D2), after which this plan gets its steps.
  Decisions marked **schema** change the config file or the metrics.
- **Milestone:** M6
- **Backlog:** K-163, K-164; the items of WebSockets and SSE are added with the ADR

## Goal

kest holds the connections PHP can't: a browser keeps a WebSocket or an SSE stream open to kest, and no PHP worker is
busy for it. PHP decides who may connect and subscribe, handles what clients send, and publishes messages that kest
delivers to every subscriber. The proxy in front can reach kest over a unix socket.

Scope: the roadmap's M6, reordered with the user on 2026-10-26. Prefork and socket relays were planned here first and
went to the icebox the same day (K-154, K-029; `research/prefork.md` § Shelved).

Exit criteria (the ADR may refine them):
- A browser holds a WebSocket or SSE connection to kest while no PHP worker is busy for it; PHP decides who may
  connect and subscribe, and a message PHP publishes reaches every subscriber.
- Connections are bounded (count, message size, a slow reader's backlog, idle time) and closed cleanly when kest stops.
- nginx reaches kest through `address: unix:/…`; a stale socket file left by a killed kest doesn't block the start.
- `make check`, `make test-integration` and `make test-matrix` are green.

## Design notes

**D1. Unix socket listeners** (K-164, schema).

```yaml
listeners:
  - name: main
    address: unix:/run/kest/kest.sock   # relative paths resolve against the config file
    socket_mode: "0660"                 # unix addresses only; default 0660
```

- **At start:** a stale socket file (connecting to it is refused) is removed. A socket someone listens on, or a file
  that isn't a socket, is an error. RoadRunner removes any socket file without checking (`research/roadrunner.md`
  § Socket relays, for its relay sockets). **At stop** the file is removed.
- **The mode is set before the socket can be reached:** kest binds under a temporary name in the same directory, sets
  the mode, then renames it into place. RoadRunner runs `chmod` after `listen`, which leaves a window.
- **No peer IP.** A peer on a unix socket has no IP, so PHP gets an empty `REMOTE_ADDR` unless `client_ip` takes the
  client's address from the proxy's header. For that, `trusted_proxies` accepts `unix`: peers on a unix socket
  listener. This is explicit, as for any other proxy.
- Address validation, duplicate checks and `/config_dump` learn the `unix:` form. A unix socket for the admin server
  is left out unless it comes free with the same code; otherwise it goes to the backlog.

**D2. WebSockets and SSE** (K-163): open. The research answers these questions, the ADR decides, and the answers
become this plan's decisions:

- **What RoadRunner offers**, checked in its source: the `centrifuge` plugin (Centrifugo as a separate server, events
  proxied to PHP workers), what became of its `websockets` and `broadcast` plugins, and the PHP libraries apps use.
- **Prior art:**
  - Centrifugo: its protocol, client SDKs, and its proxy to a backend for connect, subscribe, publish and RPC;
  - Mercure: an SSE hub, built into FrankenPHP, with a Symfony bundle;
  - the Pusher protocol, which Laravel Reverb, Soketi and Laravel Echo speak;
  - Swoole's WebSocket server.
- **The model:** kest as a hub with channels (publish/subscribe), or every connection's events passed to PHP raw, or
  both.
- **The client protocol:** kest's own, or one an ecosystem already speaks. Pusher would give Laravel Broadcasting and
  Echo with no new client code; Mercure would do the same for Symfony's SSE; Centrifugo would bring its SDKs.
- **How events reach PHP:** a short request to a pool per event (connect, subscribe, a client's message), routed like
  HTTP, lanes included.
- **How PHP publishes outside a request:** an HTTP API on a listener or on the admin server, a Pusher-style
  `POST /apps/{id}/events`, or the RPC channel of M7, which would bring part of M7 forward.
- **Bounds and lifecycle:**
  - limits: connections, subscriptions per connection, message size, a slow reader's buffer;
  - ping and idle timeouts, and draining on shutdown;
  - nginx in front: `Upgrade`, `proxy_read_timeout`, buffering off for SSE;
  - WebSockets over HTTP/2 (RFC 8441) ❓.
- **Several kest instances:** a hub in one doesn't see the subscribers of another. Fan-out through a broker (Redis,
  NATS) is likely beyond M6, but the ADR says where it would fit.
- **A WebSocket library:** the standard library has none, so the candidates are `github.com/coder/websocket` and
  `github.com/gorilla/websocket`. A new dependency needs the user's OK (ADR-0005).

## Steps

1. [x] Plan, roadmap and backlog after the reorder (prefork and socket relays shelved) — acceptance: the user
   approves the plan's direction and D1's schema.
2. [ ] K-163 research → `research/websockets.md` — acceptance: D2's questions answered, with sources at pinned
   versions; RoadRunner's behavior checked in its source (✅) or marked ❓.
3. [ ] K-164 unix socket listeners (D1), while the research and the ADR are discussed — acceptance: an e2e test
   through a unix socket; the stale file case; `client_ip` with `unix`; `docs/config.md`; the operations guide shows
   nginx's `proxy_pass http://unix:…`.
4. [ ] K-163 the ADR, `Proposed` — acceptance: the user accepts it; the backlog gets the items of WebSockets and SSE,
   and this plan its decisions and steps, the vertical slice first.
5. [ ] … the steps from the ADR.
6. [ ] Docs review (`review.md`), `architecture.md`, an operations guide section on long-lived connections behind a
   proxy — acceptance: the review's findings fixed.

## Risks / open questions

- **A hub grows without end:** presence, history, recovery after a reconnect, several instances. The ADR draws the
  line for M6, and the rest goes to the backlog.
- **A client protocol is a contract.** Speaking Pusher or Mercure ties kest to their versions and quirks; speaking
  kest's own means writing client code. The ADR weighs what each gives the PHP ecosystem.
- **A new dependency** for WebSockets, unless the protocol is written in house (it is small, but the details of
  framing and compression are where bugs live).

## Log

- 2026-10-26 — Draft. Before it, M6 was "Unix sockets and prefork" for a few hours: a plan was drafted, then the
  user questioned `relay: unix` (no faster than pipes) and prefork (complexity for a worker's boot and memory), and
  both went to the icebox. The unix socket listeners (D1) carry over from that draft.

# Plan: M6 — Unix socket listeners

- **Status:** Done (2026-10-26), merged through its PR on the user's OK. Approved by the user on 2026-10-26, D1's
  schema included. WebSockets and SSE were planned here and shelved the same day (D2).
- **Milestone:** M6
- **Backlog:** K-164; K-163 (research and ADR); the feature itself shelved as K-165

## Goal

The proxy in front reaches kest over a unix socket on the same host: no port, and a file mode that decides which local
users may connect.

Scope: what is left of M6 after the reorders of 2026-10-26. The milestone was "Unix sockets and prefork" and then
"WebSockets, SSE and unix sockets":

- prefork and socket relays went to the icebox (K-154, K-029; `research/prefork.md` § Shelved);
- WebSockets and SSE followed them once researched (D2).

Exit criteria:
- nginx reaches kest through `address: unix:/…`; a stale socket file left by a killed kest doesn't block the start.
- `make check`, `make test-integration` and `make test-matrix` are green; `make test-linux` for the Linux paths.

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
  § Socket relays, for its relay sockets). **At stop** the file is removed, if it is still kest's socket.
- **The mode is set before the socket can be reached:** kest binds under a temporary name in the same directory, sets
  the mode, then links it into place, which also refuses to replace a file that appeared meanwhile. RoadRunner runs
  `chmod` after `listen`, which leaves a window.
- **No peer IP.** A peer on a unix socket has no IP. Go reports `@` on Linux and an empty address on macOS, and kest
  clears both, so PHP gets an empty `REMOTE_ADDR` unless `client_ip` takes the client's address from the proxy's
  header. For that, `trusted_proxies` accepts `unix`: peers on a unix socket listener. This is explicit, as for any
  other proxy.
- Address validation, duplicate checks (by the resolved path) and `/config_dump` know the `unix:` form. The admin
  server stays on TCP: nothing asked for a socket there.

**D2. WebSockets and SSE: shelved** (K-163). The research is done (`research/websockets.md`). ADR-0009 proposes
holding SSE streams in kest, decided by PHP through a GRIP subset: PHP answers its own subscribe route with
`Grip-Hold: stream` and `Grip-Channel`, and publishing is an authenticated POST.

The user liked the mechanism, but shelved the feature until an application needs it: most PHP applications don't
use SSE at all. The discussion also showed what a production version needs from the start:

- **A broker** (Redis pub/sub) once there are several instances, including the two colors of a blue-green deploy.
- **Deploys as a normal case.** A deploy closes every held stream, so kest needs a staggered drain, `retry:` with
  jitter, and a lane of its own for the reconnect burst.
- **Replay.** Either the app replays from `Last-Event-ID`, or the broker keeps a short history.
- **The alternative.** A hub outside the app's containers (Mercure, Reverb, Centrifugo) keeps connections through
  app deploys.

ADR-0009 stays `Proposed`, on hold, with these notes.

## Steps

1. [x] Plan, roadmap and backlog after the reorders — acceptance: the user approves the plan and D1's schema.
2. [x] K-163 research → `research/websockets.md` — acceptance: D2's questions answered, with sources.
3. [x] K-164 unix socket listeners (D1) — acceptance: an e2e test through a unix socket; the stale file case;
   `client_ip` with `unix`; `docs/config.md`; the operations guide shows nginx's `proxy_pass http://unix:…` (it shows
   an `upstream` block instead, which keeps connections alive; both were checked, see the log).
4. [-] K-163 the ADR — proposed (ADR-0009), then shelved with WebSockets and SSE (D2).
5. [x] Docs review (`review.md`), `make test-matrix`; the PR on the user's OK — acceptance: the review's findings
   fixed.

## Risks / open questions

- None left in scope. WebSockets and SSE: see D2 and ADR-0009 when an application needs them.

## Log

- 2026-10-26 — Drafted as "WebSockets, SSE and unix sockets", after "Unix sockets and prefork" had been planned and
  shelved the same day (prefork and `relay: unix` didn't pay for their complexity).
- 2026-10-26 — K-164 done; the K-163 research done in the background; ADR-0009 proposed.
- 2026-10-26 — The user asked how it would work, then raised several instances (Nomad) and blue-green deploys that
  close held connections, and shelved WebSockets and SSE until a use case: most PHP applications don't need them. The
  research and the ADR stay as the starting point. M6 is the unix socket listeners alone.
- 2026-10-26 — Checked with real nginx 1.31.8 (`nginx:alpine` in Docker, kest built for linux/arm64): kest ran as
  the `nginx` user with `socket_mode: "0660"` on `/run/kest/kest.sock`, and nginx reached it both through
  `upstream { server unix:…; keepalive 32; }` and through `proxy_pass http://unix:/run/kest/kest.sock:/`.
  `client_ip` with `unix` took the last untrusted hop of `X-Forwarded-For`, so a client's forged entry was ignored.
- 2026-10-26 — The docs review (12 findings, all fixed; one was a bug: Linux takes 107-byte socket paths, not 108,
  and the temporary name needs room in the directory) and `make test-matrix`, then the PR on the user's OK. No milestone after M6 and no
  release yet: the user builds an application on kest and kest follows what it needs.

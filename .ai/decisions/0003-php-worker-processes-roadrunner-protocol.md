# ADR-0003: Run PHP in external worker processes speaking the RoadRunner protocol

- **Status:** Accepted (2026-10-24)
- **Date:** 2026-10-24
- **Deciders:** user, agent

## Context

The first goal is to run PHP applications "RoadRunner-style". Two families of approaches exist:

1. **External worker processes.** kest spawns `php worker.php` and exchanges framed messages over
   pipes/sockets (RoadRunner).
2. **Embedded PHP.** Link libphp through CGO and run PHP inside the Go process (FrankenPHP).

The PHP side needs a worker library. RoadRunner's (`spiral/roadrunner-worker`, `spiral/roadrunner-http`,
`spiral/goridge`, MIT) is mature and already integrated with frameworks (Symfony runtimes, Laravel Octane, Spiral).

## Decision

1. **Process model:** kest runs PHP as external, long-lived worker processes, one request at a time per
   worker; concurrency = pool size.
2. **Wire compatibility:** kest implements the worker side of the RoadRunner protocol (goridge framing,
   worker handshake/stop/error semantics, HTTP payload format) so that **unmodified RoadRunner PHP
   workers run under kest**.
3. **Config compatibility is not a goal** (see ADR-0002).
4. The protocol details are **verified against upstream sources before implementing** (K-020, K-040);
   `research/roadrunner.md` keeps the verified spec with links.
5. The `worker`/`relay` layers stay language-agnostic, so non-PHP workers (anything that speaks goridge)
   also work.

## Alternatives considered

- **Embedded PHP via CGO** — no IPC overhead and shared memory, but it needs CGO and a custom libphp
  build (ZTS), makes cross-compilation hard, and lets a PHP crash take down the whole server. It is a big
  complexity jump for an experiment. Revisit after 1.0 if IPC overhead turns out to matter.
- **Our own protocol and PHP library** — full freedom, but it means building and maintaining a PHP
  ecosystem and framework integrations from scratch, which delays the MVP considerably.
- **FastCGI to PHP-FPM** — the standard approach, but per-request bootstrap defeats the purpose.

## Consequences

- Fast path to the MVP: existing PHP libraries and framework integrations should work as-is.
- We depend on RR's protocol evolution; incompatibilities must be caught by integration tests against
  pinned `spiral/*` versions.
- IPC (serialization + pipe copy) adds per-request overhead; benchmark it in M3 (K-066).
- Licensing: RoadRunner is MIT. Reading its code is fine; any copied code keeps its copyright notice.
- Follow-ups: K-020…K-028, K-040…K-046.

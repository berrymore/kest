# Forking booted PHP workers (K-152)

Legend: ✅ measured or verified here · ❓ unverified.

A framework boots in every new worker: on every start, every replacement (`max_jobs`, `ttl`, memory), every rolling
reset, and every extra worker of a growing pool (M5, `max_workers`). Could a booted PHP process fork workers instead,
in milliseconds and with memory shared copy on write? Checked on 2026-10-25 for M5 (plan `plans/M5-pools.md` D10).
Research only: nothing is built.

## Measurement ✅

The Symfony 7.4 app of `test/e2e/testdata/symfony` (micro kernel, one controller, prod, warm cache), PHP 8.5 CLI
without OPcache. A script (not in the repository) boots the kernel, serves one request, forks 8 children, and each
child serves two requests. Memory is PSS (`/proc/<pid>/smaps_rollup`), so shared pages count once.

| | Linux (Docker, `php:8.5-cli` + `pcntl`, arm64) | macOS (PHP 8.5.12) |
|---|---|---|
| A worker booted on its own: autoload, boot, first request | 35–43 ms | 15 ms |
| PSS of such a worker | 27.5 MB | — (no PSS on macOS) |
| Forking 8 children of a booted parent | 1.3 ms | 3.9 ms |
| A child's first request / second request | 1–2 ms / 0.05–0.09 ms | 0.8–1.4 ms / 0.05–0.07 ms |
| PSS: the parent / the 8 children together | 14.9 MB / 21.2 MB | — |

So 9 processes take 36 MB forked against about 248 MB booted one by one (7 times less), and a new worker is ready
in about a millisecond instead of the boot. This app is tiny: real applications boot in tens to hundreds of
milliseconds even with a warm cache ❓, which makes the gap wider. A child's first request costs about a
millisecond more than later ones (pages copied on first write).

`mt_rand` differed between the children because the parent never used it before forking; a parent that does would
give every child the same sequence.

## What it would take

- **A relay per child.** Children can't share the parent's pipes. roadrunner/worker connects to `RR_RELAY`, which
  may be `unix://…` or `tcp://…` ✅ (`research/roadrunner.md`), so each child would connect to a unix socket that
  kest listens on for the pool (K-029, socket relays), and send its pid in the handshake as today.
- **A hook in the worker script.** The fork must come after the boot and before `Worker::create()`, which opens the
  relay. That is a change to the application's worker script (a small PHP library from kest, such as
  `Prefork::run(fn () => …the worker loop…)`), so prefork would be an opt-in mode, not something an unmodified
  RoadRunner worker gets. The parent (a "template") would stay up, talk to kest over its pipes (control frames:
  "fork one"), and fork on request.
- **Process management.** The children are the template's, not kest's: kest can't `wait()` for them. It would know
  them by their handshake pid and their socket (EOF when they die), kill them by pid or by the template's process
  group, and replace the template on a rolling reset (one boot, then forks). On Linux, `Pdeathsig` covers the
  template but not its children; roadrunner/worker ends when its relay closes ✅ (exit 255,
  `research/roadrunner.md` § Socket relays), which stops the children when kest goes away.
- **Logs.** Children inherit the template's stderr: kest would see their lines but not which pid wrote each, so
  `worker_pid` in the logs is lost unless the library prefixes lines.
- **State shared by the fork.** Connections opened during the boot (database, Redis, HTTP keep-alive) are shared by
  every child and must be reopened in each; Doctrine and Laravel connect lazily, which helps ❓. Random generators
  seeded before the fork (`mt_rand`, `Random\Randomizer` objects) repeat in every child unless reseeded. File locks
  and signal handlers carry over. `pcntl` is needed (Linux and macOS; the official Docker images build it with
  `docker-php-ext-install pcntl`).

## Conclusion

Worth building, as an opt-in mode: it makes a growing pool react in milliseconds instead of a boot, makes resets
and `max_jobs` replacements cheap, and cuts memory several times. It needs socket relays first (K-029), a small PHP
library, and changes to how kest owns processes (pids it didn't spawn). Next: an ADR when it is scheduled (K-154),
after measuring a real application's boot (Laravel and Symfony skeletons with OPcache on and off).

## Shelved (2026-10-26)

Planned for M6, then shelved with the user before any code (K-154 in the icebox). A worker's boot costs tens of
milliseconds, and only a growing pool, replacements and resets pay it, none of which an application needs to start.
The real gain would be memory density: separate CLI workers each hold their own heap and their own OPcache, while
forked ones would share the template's ❓ (not measured on real applications). Against that stand processes kest
didn't spawn, a PHP package, macOS quirks ❓, and fork safety that falls on the application: a database connection
opened during the boot and shared by the children corrupts data quietly, and PHP frameworks don't expect a fork after
the boot. Revisit when a deployment runs short of memory, or when a framework designed to be fork-safe can make it
safe by default.

The design sketched for M6, as a starting point:

- **A template per pool**, started over pipes like any worker. The worker script opts in with a call such as
  `Kest\Prefork::run(fn () => …the worker loop…)` after the boot, which just runs the closure outside kest's prefork
  (under RoadRunner too). The template answers kest's handshake (`"prefork": true` in both directions, so an
  unmodified script is caught at once), serves no requests, and forks on control frames: kest sends
  `{"fork": {"relay": "unix://<path>"}}`, the template answers `{"forked": <pid>}` and reports `{"exited": <pid>,
  "status": <n>}` as it reaps; kills go through it (`{"kill": <pid>}`), with the template's process group as the
  fallback.
- **A one-shot unix socket per child.** kest listens on a new socket in a private directory before each fork, accepts
  exactly one connection, then removes it. The connection is the child's, with no pid matching. RoadRunner's
  pid-matched listener can't take forked children (`research/roadrunner.md` § Socket relays). The peer credentials
  (`SO_PEERCRED`, `LOCAL_PEERPID`) confirm the pid. A child whose socket closes exits (255), so children don't outlive
  kest.
- **The child's first steps:** close the template's control pipes (stdout pointed at stderr), set `RR_RELAY`, reseed
  `mt_rand`, run the closure. Children log through the template's stderr, under its pid.
- **Lifecycle:** a rolling reset boots one new template and forks from it; if the template dies, its children keep
  serving and new workers wait for a new one. PSS on Linux shows what is shared.

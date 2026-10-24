# Plan: M1 — Worker pool and relay

- **Status:** Done (2026-10-25; decisions D1–D4 accepted 2026-10-24)
- **Milestone:** M1
- **Backlog:** K-020…K-028 (K-029 deferred)

## Goal

`internal/worker` runs a fixed-size pool of long-lived processes that speak the RoadRunner worker protocol
over pipes: spawn, handshake, exec with deadlines, graceful stop, kill, supervision limits, and respawn with
backoff. A PHP integration test keeps N unmodified `roadrunner/worker` echo workers serving under concurrent
load while random workers get `kill -9`. Nothing is wired into HTTP or the config file yet; M2 does that
through the `php_workers` cluster (K-041).

## Design notes

Protocol facts: `research/roadrunner.md` (verified against RR v3.0.1, goridge v4.0.0, pool v2.0.0,
`roadrunner/worker` 3.8.2, and by experiment). Process model: ADR-0003. Layering: `worker` → `relay`, nothing
else (no HTTP, no YAML, no `internal/config`).

### `internal/relay`: frame codec and connection (K-021, K-022) ✅

```go
type Frame struct {
	Flags   Flags    // byte 1: FlagControl, FlagError, Codec* bits
	Stream  Stream   // byte 10: StreamMore, StreamStop, StreamPing, StreamPong (codec only in M1)
	Options []uint32 // at most MaxOptions (10)
	Payload []byte
}

func AppendHeader(dst []byte, f *Frame) ([]byte, error)
func AppendFrame(dst []byte, f *Frame) ([]byte, error)
func NewWriter(w io.Writer) *Writer              // WriteFrame: one Write up to 32 KiB, header + payload above
func NewReader(r io.Reader, maxPayload int) *Reader // ReadFrame: fresh Options/Payload per frame, owned by the caller
func NewConn(r io.Reader, w io.Writer, maxPayload int) *Conn // Send / Receive; pipes now, sockets later (K-029)
```

- Encode writes version 1, HL, LE lengths, CRC32-IEEE of bytes 0–5; byte 11 is written as zero. Decode checks
  the CRC, then the version, then 3 ≤ HL ≤ 13, and enforces `maxPayload` **before** allocating.
- Errors: `*ProtocolError{Reason, Data}` (Data = the 12 header bytes as read: this is how PHP output on stdout
  shows up), `ErrFrameTooLarge`, `io.EOF` before a frame, `io.ErrUnexpectedEOF` inside one.
- No deadlines and no `Close` in the relay: a timeout in the worker layer kills the process, which ends blocked
  I/O. We never try to resynchronize a stream after an error.

### `internal/worker`: process, worker, pool, supervisor (K-023…K-027)

**Process.** A small interface between the worker logic and the OS, so that time-based tests can run on an
in-process fake:

```go
type process interface {
	Pid() int
	Conn() *relay.Conn
	Kill()                 // SIGKILL to the process group; no-op after exit
	Done() <-chan struct{} // closed once the process has exited
	Err() error            // exit error
	Stderr() string        // last lines of stderr, for error messages
	Close()                // releases the streams, after Done
}
```

The real implementation (`execProcess`, ✅ step 3) uses `os/exec` with:
- an **explicit environment**: exactly `Config.Env` plus `RR_RELAY=pipes`. Whether to inherit kest's
  environment by default is a config-policy question for M2 (K-041);
- `Setpgid` (terminal signals don't reach workers), SIGKILL to the whole group (no orphaned grandchildren),
  `Pdeathsig: SIGKILL` on Linux (workers die with kest even on `kill -9`; on macOS they see EOF on stdin and exit);
- its own `os.Pipe`s for stdin/stdout, so `Wait` never closes stdout while a response is still unread;
- `exec.CommandContext` with the owner's lifetime context and a group-kill `Cancel`, and `WaitDelay` (1s), so a
  grandchild that keeps stderr open can't block `Wait`;
- stderr read line by line (lines capped at 64 KiB) and logged at info with `cluster` and `worker_pid`; the last
  4 KiB are kept for error messages.

**Worker.** One process plus its state (architecture.md §5). Handshake: send `CONTROL|JSON {"pid": os.Getpid()}`,
expect a `CONTROL` frame with a pid > 0, within `StartTimeout` (this also covers the application's boot). A
reported pid different from the child's is logged at debug (wrappers such as `sh -c`); signals and RSS always
use the child pid. A failed start reports the exit status, the unexpected stdout bytes and the stderr tail.

**Pool: one goroutine per slot.** The pool owns N slot goroutines. Each one runs its slot forever: spawn (with
backoff) → handshake → serve jobs → retire or crash → clean up → spawn again, until the pool stops. All
goroutines have one owner (the pool), one stop signal and are awaited in `Stop`.

- Jobs are handed over on one **unbuffered** `jobs` channel: a ready slot receives, a caller sends. A handoff only
  happens when a worker is free, waiting callers are served in FIFO order, and there is no idle list to keep
  consistent. The wait is bounded by `AllocateTimeout`, the caller's context and `MaxQueueSize` (an atomic count of
  waiting callers; over the limit → `ErrQueueFull` at once).
- A ready slot `select`s on: a job; its `IdleTTL` timer; its `TTL` timer; a retire signal from the supervisor;
  pool stop. TTL and idle TTL are exact timers, not polling, which makes them easy to test with `synctest`.
- **Exec** runs in the slot goroutine: send the request, read one response, under `ExecTTL` enforced with
  `context.AfterFunc(kill)`. The caller waits for the result or its own context. If the caller gives up (client
  gone, route timeout), the worker is **not** killed: it finishes, bounded by `ExecTTL`, and the result is
  dropped. Killing on cancellation would let clients that disconnect kill workers.
- After each job the slot checks `MaxJobs` (with RR's +0…14 % jitter), `TTL` and the supervisor's memory mark.
  If a limit is hit, it stops the worker gracefully and spawns a replacement. A worker is never recycled
  mid-request except on `ExecTTL` or a crash.
- Outcomes and errors (M2 maps them to HTTP statuses):

  | Worker behavior | Worker | Caller gets |
  |---|---|---|
  | normal response | kept | `Payload` |
  | `ERROR` frame | kept | `*JobError{Message}` (→ 500) |
  | `{"stop":true}` instead of a response | replaced | request retried once on another worker; a second stop → `ErrStopRequested` |
  | crash, EOF, CRC/protocol error, `STREAM` response (unsupported in M1) | killed, replaced | `*ProcessError` with exit status and stderr (→ 502) |
  | `ExecTTL` exceeded | killed, replaced | `*ProcessError` wrapping `ErrExecTimeout` (→ 504) |
  | no worker within `AllocateTimeout` | — | `ErrNoWorker` (→ 503) |
  | `MaxQueueSize` waiters already | — | `ErrQueueFull` (→ 503) |
  | pool stopping | — | `ErrStopped` (→ 503) |

- **Respawn** with exponential backoff and jitter per slot: 100 ms doubling up to 30 s. It resets once a worker
  has run for 10 s or completed a job, so a script that boots and then crashes on every request still backs off.
  The pool never panics. `Health()` reports healthy (N live), degraded (0 < live < N) or unhealthy (none live)
  for the admin API and `/ready` later (M3).
- **Start(ctx)** spawns N workers in parallel and returns once all are ready. Any failure fails `Start`
  (fail fast: a broken worker script is a startup error, as in RR) and kills the others.
- **Stop(ctx)**: new and waiting `Exec` calls get `ErrStopped`; busy workers finish their job; every worker gets
  the stop frame and `DestroyTimeout` to exit, then SIGKILL; `Stop` returns when all slot goroutines are gone.
  An expired `ctx` kills everything at once (logged as a warning, as in M0).

**Supervisor (memory only).** One goroutine per pool samples RSS of all live workers every `WatchTick`. An idle
worker over `MaxWorkerMemory` is retired at once; a busy one is marked and retired after its job.
RSS sampling, pure Go, no new dependency (decision D1): Linux reads `/proc/<pid>/statm`; macOS runs one batched
`ps -o pid=,rss= -p <pid,…>` per tick (gopsutil, which RR uses, also execs `ps` on macOS, once per worker); other
OSes reject `MaxWorkerMemory` when the pool is created. The sampler is an interface, so tests inject values.

### Go-only configuration in M1 (decision D2)

```go
type Config struct {
	Command         []string
	Env             []string      // exact worker environment; RR_RELAY=pipes is added
	Dir             string
	Workers         int           // default runtime.NumCPU()
	MaxJobs         uint64        // 0 = unlimited
	MaxQueueSize    int           // default 1000 (RR: unbounded)
	AllocateTimeout time.Duration // default 60s
	StartTimeout    time.Duration // default 60s: spawn + handshake (application boot)
	DestroyTimeout  time.Duration // default 10s (RR: 10s per worker, hard-coded)
	MaxFrameSize    int64         // default 64 MiB (RR: unbounded)
	TTL, IdleTTL    time.Duration // 0 = off
	ExecTTL         time.Duration // default 60s (RR: unlimited)
	MaxWorkerMemory int64         // bytes, 0 = off
	WatchTick       time.Duration // default 5s, memory sampling only
}
```

Plain Go types, `Validate()` with field names. M2's `php_workers` config struct (YAML, `config.Duration`,
`config.ByteSize`, located errors) maps onto it; the YAML names get reviewed then (architecture.md §3 has a draft).

### Test strategy

- **Codec:** golden frames as observed from the real PHP worker on 2026-10-24 (handshake reply, response with
  options `[7]`, `ERROR` frame, `{"stop":true}` response), round trips, malformed input (bad CRC, HL < 3,
  truncated header/options/payload, oversize payload). `FuzzReceive`: never panics, never allocates past
  `MaxPayload`, re-encoding a decoded frame gives the same bytes. Benchmarks with `b.Loop()` for 1 KiB / 64 KiB /
  1 MiB payloads, including allocations.
- **In-process fake worker** (`internal/worker/workertest`, K-024): a goroutine that speaks the PHP worker's side of
  the protocol over `net.Pipe`, with scriptable behavior: echo, slow, hang, soft error, stop request, crash
  before/after reading, bad frame, garbage before the handshake, fail the handshake, fail N spawns then succeed,
  memory growth through a fake sampler. Pool, worker and supervisor tests run inside `synctest.Test`, so TTLs,
  `ExecTTL`, `AllocateTimeout`, backoff and `WatchTick` run on the fake clock with no sleeps. (`synctest` only
  advances time when every goroutine is durably blocked; real pipes and processes never are, so only the
  in-process fake is used inside a bubble.)
- **Real processes:** the test binary re-runs itself as a fake worker (`TestMain` switches on an env variable,
  the standard `os/exec` helper-process pattern; same behaviors as above). It covers the exec-based process:
  environment, stderr logging, exit status, SIGKILL of the process group, garbage on stdout, `WaitDelay`.
- `leaktest.VerifyTestMain` in `relay` and `worker`.
- **PHP integration** (`-tags integration`, K-028): `internal/worker/testdata/php/` with `composer.json` requiring
  `roadrunner/worker` (pinned, `composer.lock` committed, `vendor/` ignored) and an echo `worker.php`.
  `make test-integration` runs `composer install` first; the test skips with a clear message when `php` or
  `vendor/` is missing. Scenarios: 4 workers × 2000 execs from 32 goroutines while a random worker gets SIGKILL
  every ~50 ms: only requests in flight on a killed worker fail (`*ProcessError`), the pool returns to 4 live
  workers, no process is left behind after `Stop`. Plus `MaxJobs` recycling and a soft error.

### Out of scope

Streaming responses (byte-10 bits are encoded and decoded, but a `STREAM` response is a protocol error until Q-C),
socket/TCP relays (K-029, D3), rolling reset (M3, K-063; the slot design allows it), dynamic pool size, running
workers as another user, RR's `on_init` command, any config file or HTTP surface.

## Steps

1. [x] **Research** (K-020) — `research/roadrunner.md` verified with pinned links and an experiment (2026-10-24).
2. [x] **Frame codec** (K-021) — `internal/relay`: `Frame`, encode/decode, limits, typed errors, golden frames,
   fuzz, benchmarks. Acceptance: tests and a 2-minute fuzz run are clean; `make check` green.
3. [x] **Pipe relay and process** (K-022, K-023) — `relay.Pipe`; the exec-based process (env, process group,
   Pdeathsig, `WaitDelay`, stderr logger). Acceptance: helper-process tests for env, stderr, exit, group kill.
4. [x] **Fake worker** (K-024) — `workertest`: in-process fake over `net.Pipe` and the helper-process mode, one
   behavior table for both. Acceptance: every behavior listed above is exercised by at least one test.
5. [x] **Worker** (K-023) — handshake with timeout and diagnostics, exec with `ExecTTL`, stop (frame →
   `DestroyTimeout` → SIGKILL), kill, states. Acceptance: `synctest` tests for each transition in §5.
6. [x] **Pool** (K-025) — slots, `jobs` handoff, `MaxQueueSize`, `AllocateTimeout`, `MaxJobs` with jitter, TTL
   and idle timers, stop-request retry, error types, `Start`/`Stop`. Acceptance: one test per limit and per row of
   the outcome table; concurrent load test under `-race`.
7. [x] **Supervisor** (K-026) — memory sampling loop; Linux `/proc` and macOS `ps` samplers. Acceptance: fake
   sampler tests in `synctest`; a real-sampler test against the test's own child process on the host OS.
8. [x] **Respawn and health** (K-027) — per-slot backoff with jitter and reset rule; `Health()`. Acceptance:
   a worker that always fails backs off to 30 s on the fake clock; the pool recovers when it starts working.
9. [x] **PHP integration** (K-028) — testdata, `composer.lock`, Makefile, the kill -9 load test. Acceptance:
   `make test-integration` green locally on PHP 8.5; the roadmap's M1 exit criteria hold.
10. [x] **Docs** — architecture.md §4, §5 and §7 match the code; AGENTS.md command table
    (`test-integration` exists now); backlog and status.

Commits: 1–3 for the milestone, only when the user says so (e.g. relay + worker core, then pool +
supervisor + integration).

## Decisions (accepted by the user on 2026-10-24, as recommended)

- **D1. RSS sampling** — `/proc/<pid>/statm` on Linux, one batched `ps` per tick on macOS, unsupported elsewhere;
  no new dependency. Alternatives: gopsutil (large dependency tree, and it execs `ps` on macOS anyway), libproc via
  CGO (breaks pure Go), hand-written `x/sys`-style libSystem trampolines (fragile).
- **D2. No config surface in M1** — the pool is an internal Go API (`worker.Config`); the YAML schema arrives with
  `php_workers` in M2 and is reviewed then.
- **D3. Pipes only** — K-029 (unix socket / TCP relays) stays deferred until something needs it.
- **D4. Bounded defaults where RR has none** — `ExecTTL` 60s (RR: unlimited), `MaxQueueSize` 1000 (RR: unbounded),
  `MaxFrameSize` 64 MiB (RR: 4 GiB), `DestroyTimeout` 10s. All configurable in M2.

## Outcome / deviations

- Exit criteria met: `TestPHPPoolSurvivesKill9` keeps 4 `roadrunner/worker` 3.8.2 workers serving ~200k jobs in
  2 s under `-race` while ~40 get SIGKILL; only jobs on a killed worker fail, the pool recovers, `Stop` is graceful
  and leaves no process. Every limit has a `synctest` test; leak checks are clean; Linux via `make test-linux`.
- No `relay.Relay` interface: one concrete `relay.Conn` serves pipes and the in-process fake.
- `ReadFrame` allocates a fresh payload per frame (no shared read buffer); request frames copy context + body
  (K-069 if benchmarks show it matters).
- Respawn/backoff and health landed with the pool (steps 6 and 8 in one go).
- `worker.Info` instead of `WorkerInfo`. Config defaults are explicit (`DefaultConfig`), zero means "off".
- `idle_ttl` also replaces a worker that never got a job (RR doesn't).
- Worker stderr lines carry `worker_pid`; `cluster` comes with M2's logger.
- New: `make test-linux` (unit tests in Docker, `golang:1.27`); `make test-integration` runs `composer install`.
- Not done here: PHP on Linux and the PHP version matrix (K-047, M2).

## Risks / open questions

- `ps` on macOS costs one fork per tick per pool; fine for development (macOS is not a production target), and
  `WatchTick` bounds it.
- `Pdeathsig` on Linux fires when the OS *thread* that forked the child exits, not the process. Go's runtime only
  ends a thread when a goroutine exits while locked with `LockOSThread`, so spawning from an unlocked goroutine
  is safe. ✅ `TestProcessDiesWithParent` passes on Linux (`make test-linux`). Never spawn workers from a goroutine
  that locks its OS thread.
- The handshake timeout also covers PHP application boot; slow frameworks may need a larger `StartTimeout`.
- `synctest` + `net.Pipe`: expected to be durably blocking (channels and timers inside the bubble); verify in
  step 4 before building every test on it.

## Log

- 2026-10-24 — K-020 done: RoadRunner is now v3 (goridge v4, pool v2), PHP packages renamed to `roadrunner/*`;
  wire format unchanged from goridge v3. Protocol checked by experiment against `roadrunner/worker` 3.8.2.
  Plan drafted.
- 2026-10-24 — The user accepted D1–D4 as recommended; plan is Active. Work moved to branch `feat/m1-worker-pool`.
- 2026-10-24 — Step 2 done: `relay.Reader`/`Writer`, golden frames from goridge v4 and from the PHP worker, two fuzz
  targets (2 min each, clean; the reader target also runs with a fixed checksum to get past the CRC), benchmarks
  (write: 0 allocs; read: 2 allocs). Deviation: `ReadFrame` allocates a fresh payload per frame instead of
  reusing a buffer, because the worker hands responses to another goroutine; no copy is needed then.
- 2026-10-24 — Step 3 done: `relay.Conn`, `execProcess`, `stderrLog`. The relay has no `Relay` interface: one
  concrete `Conn` serves real pipes and the in-process fake (`net.Pipe`). Pdeathsig and the group kill verified on
  Linux with the new `make test-linux` (Docker, `golang:1.27`); the whole unit suite passes there too. Helper
  processes set `GORACE=atexit_sleep_ms=0`: a `-race` binary otherwise sleeps 1s before exiting.
- 2026-10-25 — Step 4 done: `workertest` (in-process `Fake` over `net.Pipe`, `RunProcess` for the helper process;
  commands sleep/hang/error/stop/crash/exit/garbage/stream/big/memory/no-options; boot options). Verified that
  `net.Pipe` blocking is durable in `synctest`. `net.Pipe` doesn't buffer, so the fake reads the handshake before
  writing boot output (kest sees the same bytes).
- 2026-10-25 — Steps 5–8 done (respawn and health came with the pool: they live in the slot loop). `worker.go`:
  `handshake`, `execute`, `stopProcess`, error types (`ProcessError`, `JobError`, sentinels); kill races decided by
  `timer.Stop()` results, not flags. `pool.go`: slots, `Exec`, `Start`/`Stop`, `Workers()`/`Health()`, backoff;
  `config.go`: `DefaultConfig`/`Validate` (zero means "off", defaults are explicit). `supervisor.go` + `memory_*.go`.
  `WorkerInfo` is `worker.Info` (revive: stutter). Idle TTL counts from when a worker became ready, so an unused
  worker is replaced too (RR never replaces a worker that was never used). Tests: synctest on the in-process fake
  for every limit and outcome; `TestPoolSurvivesKilledWorkers` with real helper processes (~35k jobs/0.5 s under
  `-race`, one failed job per SIGKILL); 30 runs in a row clean; Linux via `make test-linux` clean.
- 2026-10-25 — Steps 9–10 done: PHP integration tests (echo, soft error, stop request, max_jobs, exec_ttl,
  memory limit, kill -9 under load) and docs. Found while testing: worker stderr lines lacked `worker_pid` (fixed);
  PHP answered 4000 jobs before the first kill, so the kill test runs for a fixed 2 s instead. Plan done.

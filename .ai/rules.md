# Engineering rules

Rules for anyone (agent or human) writing code in kest. If the user explicitly asks for something that
conflicts with a rule, follow the user, and if the instruction is durable, update this file.

## 1. Communication and workflow

- Talk to the user in **Russian**. Every artifact is in **English**: code, comments, identifiers, docs,
  commit messages, PR descriptions, issues.
- Non-trivial task → short plan first (in chat, or in `plans/` for multi-session work), then implement.
- Work in small, verifiable steps: build and test after each one.
- Don't silently expand scope. Problems or ideas outside the task go to `backlog.md`.
- Ask before: adding a dependency, changing the config schema, changing an ADR status, deleting files,
  committing. Never push, force-push or rewrite history unless asked.
- Report honestly: if tests fail or a step was skipped, say so.

## 2. Go code

- **Go 1.27.** Use modern idioms: `for i := range n`, `min`/`max`, iterators (`iter.Seq`), `slices`/`maps`,
  `sync.WaitGroup.Go`, `t.Context()`, `b.Loop()`, `testing/synctest`.
- Everything lives in `internal/` by default. A public `pkg/` API (e.g. for third-party extensions) needs an ADR.
- One package per responsibility; avoid `util`/`common` grab-bags.
- Accept interfaces, return concrete types. Define small interfaces on the consumer side.
- No mutable package-level state. The one exception is the extension registry: filled in `init()`,
  read-only after startup.
- No `panic` for expected failures. Panics are reserved for programmer errors (e.g. duplicate
  extension registration in `init()`).
- Constructors validate their input; components are fully usable once constructed and started.
- Config structs use snake_case `yaml` tags. Durations are Go duration strings (`30s`); sizes are
  human-readable (`128MB`), parsed by `config.Duration` and `config.ByteSize`.
- Format with `gofmt`; code must pass `go vet` and `golangci-lint` (config: K-002).

## 3. Concurrency and lifecycle

- Every goroutine has an **owner**, a **stop signal** (context or channel close) and is **awaited** on
  shutdown (`sync.WaitGroup` / `errgroup`). No fire-and-forget.
- Anything that blocks or does I/O takes `ctx context.Context` as its first parameter and honors cancellation.
- Don't store a `context.Context` in structs, except a component's own run context created in
  `Start` and cancelled in `Stop` (document it).
- Channels transfer ownership; mutexes protect state. If a type holds more than one mutex, document the lock order.
- All I/O with worker processes has a deadline. A stuck worker must never hang a request forever.
- Long-lived components implement `Start(ctx) error` / `Stop(ctx) error` (see architecture.md §4).

## 4. Errors and logging

- Wrap errors with context: `fmt.Errorf("start cluster %q: %w", name, err)`. Messages start lower-case,
  without trailing punctuation.
- Use sentinel or typed errors for conditions callers branch on; check with `errors.Is` / `errors.As`.
- Handle an error (log, recover, retry) or return it — never both.
- Logging uses `log/slog`. The logger is passed in through constructors; library code never calls `slog.Default()`.
- Consistent log keys: `component`, `listener`, `cluster`, `route`, `worker_pid`, `request_id`, `err`.
- Never log request bodies, credentials or full environment variables by default.

## 5. Configuration

- Strict decoding: unknown fields are errors.
- Every option has a documented default, validation and a test.
- Validation errors carry the YAML path (and line when available):
  `listeners[0].http.virtual_hosts[0].routes[2].route.cluster: unknown cluster "foo"`.
- Cross-references (route → cluster, etc.) are validated before anything starts.
- Schema changes need the user's OK. Before 1.0, breaking changes are allowed but go into the changelog.

## 6. Testing

- `go test -race ./...` must pass before work counts as done.
- Unit tests sit next to the code; table-driven where it helps readability.
- Integration tests use `//go:build integration`. Tests that need PHP call `exec.LookPath("php")` and
  `t.Skip` with a clear message when it's missing.
- Time-dependent logic (TTLs, timeouts, backoff) is tested with `testing/synctest`, not `time.Sleep`.
- Pool and supervisor tests use a fake worker written in Go, so they don't need PHP.
- Parsers and decoders (frame codec, config expansion, payload decoding) get fuzz tests.
- Hot paths (relay, codec, routing) get benchmarks (`b.Loop()`).
- No goroutine leaks: every package that starts goroutines has
  `func TestMain(m *testing.M) { leaktest.VerifyTestMain(m) }` (`internal/leaktest`, built on goleak).

## 7. Dependencies

- Stdlib first. A new dependency needs: why the stdlib is not enough, maintenance status, license
  (MIT / BSD / Apache-2.0 only), and no CGO. Record the reasoning in an ADR or in `status.md`, and get the user's OK.
- Approved dependencies are listed in ADR-0005.

## 8. Security defaults

- The admin server binds to `127.0.0.1` by default.
- Request header and body sizes are bounded by default.
- Config dumps redact secrets (fields marked sensitive, env values).
- Worker processes get an explicit, documented environment.

## 9. Git

- **Never commit without asking.** When you think a commit is due, stop and ask the user. They either
  allow it or tell you to finish the work first without committing.
- **Coarse commits.** No frequent micro-commits. A large task is split into 1–3 commits along major lines.
  A milestone the user has said to commit is committed once it is fully done, not part by part.
- Conventional Commits: `feat(config): strict decoding`, `fix(worker): …`. No binaries or generated
  artifacts in git.
- **Branches.** Work happens on `main` for now. Once feature branches and PRs start, they are merged into
  `main` with **squash merge** only.
- **No AI attribution** in commits, PRs, code or docs: no `Co-Authored-By:` trailers, no
  "Generated with …" footers, no mentions of an AI agent. This overrides tool defaults.
- Remote `origin` = `git@github.com:berrymore/kest.git`, default branch `main`. Never push unless asked.

## 10. Codebase patterns (established in M0)

- **Config errors** are `config.Errors` (a list of `*config.Error` with file, line, column, YAML path).
  Collect them with `errs.Append(err)` and return `errs.Err()`, so that every problem is reported at once.
  Extensions report through `ext.Errorf(field, …)` or `*config.FieldError` from `Validate`.
- **Tests assert exact locations** (`kest.yaml:12:31: listeners[0]…: message`). If a column looks off,
  count it again before "fixing" the code: so far the code was right every time.
- **Components** bind or spawn in `Start(ctx)`, and stop gracefully in `Stop(ctx)` within its deadline. An
  expired deadline forces a close and logs a warning; it is not an error by itself.
- **Fatal errors after Start** go through a callback (`onFatal` / `server.fail`); the server then shuts down.
- **Test helpers** take `t *testing.T` and call `t.Helper()`. Use `t.Context()`, `t.TempDir()`, and
  `http.NoBody` for request bodies; linters enforce most of this.
- **No sleeps for synchronization** in tests: use channels, and `waitFor` only for external observation
  (HTTP polling). Time-based logic in M1+ uses `testing/synctest`.

## 11. Documentation

- User docs go in `docs/` (config reference, guides); agent docs go in `.ai/`.
- Update docs in the same change as the code they describe.
- Every config option appears in the config reference once `docs/` exists.
- Mark unverified claims about other projects as ❓ (see `.ai/README.md`).

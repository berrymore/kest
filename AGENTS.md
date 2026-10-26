# AGENTS.md

Guidance for AI coding agents (Claude Code, Codex, Cursor, …) working on **kest**.
It is also the shortest path for a human to understand how the project is run.

## What kest is

kest is an application server written in Go, in the spirit of RoadRunner, configured like Envoy:
one declarative YAML file describing a graph of **listeners → HTTP filters → routes → clusters**.

- **First target:** serve PHP applications through pools of long-lived PHP worker processes,
  compatible with the RoadRunner PHP worker ecosystem (ADR-0003).
- **Later:** a WASM sandbox (wazero) for running small, isolated, fast functions and filters (ADR-0006).
- **Stage:** early experiment with the explicit goal of reaching production quality.

Vision, goals, non-goals and glossary: `.ai/context.md`.

## Session protocol

**At the start of every session**

1. Read `.ai/status.md` — current phase, work in progress, next steps, open questions.
2. If you don't remember how `.ai/` is organized, read `.ai/README.md`.
3. Before touching a subsystem, read the matching section of `.ai/architecture.md` and the ADRs it links.
4. Take work from the active plan in `.ai/plans/` or from `.ai/backlog.md`.
   For non-trivial or ambiguous tasks, agree on scope with the user before writing code.

**At the end of every session (or after a meaningful chunk of work)**

- Update `.ai/status.md`: what changed, what's next, new open questions, one session-log line.
- Tick/add items in `.ai/backlog.md` and in the active plan.
- Significant decision made → write an ADR in `.ai/decisions/` (status `Proposed` until the user accepts).
- Architecture changed → update `.ai/architecture.md` in the same change.

Docs that drift from the code are worse than no docs. Keep them true, short and current. `make docs-check`
catches the mechanical drift on every `make check`; the docs review (`.ai/review.md`) catches the rest before a
milestone merges.

The user may run `/clear` at any point after a handoff. `.ai/status.md` → **Next up** must always let a
fresh session continue without the chat history: concrete first step, files to read, open decisions.

## Commands

Keep this table in sync with the Makefile (`make help` lists every target).

| Task | Command |
|---|---|
| **Everything before "done"** | `make check` (build, vet, lint, unit tests with `-race`, e2e) |
| Build | `make build` → `bin/kest` |
| Unit tests | `make test` (`go test -race ./...`) |
| End-to-end tests | `make test-e2e` (builds the binary with `-race`, runs it against configs) |
| Integration tests (need `php`, `composer`) | `make test-integration` (`make php-deps`, then the `integration`-tagged tests and the PHP e2e tests) |
| PHP packages of test projects and examples | `make php-deps` (composer install in every directory of `PHP_PROJECTS`) |
| Unit tests on Linux (needs Docker) | `make test-linux` (Linux-only code paths; run when touching them) |
| PHP version matrix on Linux (needs Docker) | `make test-matrix` (`make test-integration` on PHP 8.3, 8.4, 8.5; image in `test/docker`) |
| Benchmark against RoadRunner | `RR=/path/to/rr make bench` (`test/bench`; results in `docs/benchmarks.md`) |
| Docs drift checks | `make docs-check` (links, make targets, IDs, layout, config reference; also part of `make test`) |
| Milestone docs review | before the PR and before the merge: `.ai/review.md` |
| Vet | `make vet` (`go vet` with the `integration` and `e2e` tags) |
| Lint | `make lint` (golangci-lint 2.15.0 pinned; config in `.golangci.yml`) |
| Format | `make fmt` |
| Clean | `make clean` (removes `bin/`) |
| Run | `./bin/kest serve -c examples/hello/kest.yaml` |
| Validate a config | `./bin/kest validate -c FILE [-dump]` |

Build tags: `integration` (needs PHP), `e2e` (real binary); PHP e2e tests need both. Lint and vet run with both.

## Repository layout

Module `github.com/berrymore/kest`, Go 1.27. Package responsibilities and the target layout are in
`.ai/architecture.md` §7; follow it when adding packages.

```
AGENTS.md            this file (CLAUDE.md imports it)
README.md            user-facing overview
.ai/                 agent workspace: status, rules, architecture, roadmap, backlog, plans, ADRs, research, review
.claude/agents/      docs-reviewer: the reviewer of the milestone docs review (.ai/review.md)
cmd/kest/            CLI: serve, validate, version
internal/config/     YAML loading, env expansion, strict decoding, defaults, validation, dump
internal/registry/   compiled-in extension categories and factories
internal/server/     builds the component graph; start/stop order; drain
internal/httpx/      HTTP filter chain and router
internal/listener/   HTTP listeners on TCP and unix sockets
internal/filters/    built-in HTTP filters, one package each: requestid/, accesslog/, recovery/, clientip/, headers/, cors/, compression/, static/, sendfile/
internal/cluster/    cluster interface and registry category; types: phpworkers/
internal/plugin/     plugin registry category (ADR-0010); types: kv/, external/; wire/ (the external protocol)
internal/rpc/        the RPC server: goridge RPC from PHP workers, routed to plugins by name
internal/rrhttp/     HTTP ↔ RoadRunner HTTP payload (request context JSON/protobuf, forms, uploads, responses)
internal/relay/      goridge frame codec and connection (RoadRunner worker protocol)
internal/proc/       child processes: process group, death with kest, output logged line by line
internal/worker/     worker processes, pool, supervisor; workertest/ fakes, testdata/php/ for integration
internal/admin/      admin API
internal/metrics/    metrics core and Prometheus text exposition (ADR-0007); metricstest/ reads it back in tests
internal/logging/    slog setup
internal/buildinfo/  version information
internal/leaktest/   goroutine-leak check for tests (TestMain)
pkg/plugin/          the plugin SDK (public): Plugin, Call, Serve for external plugins
examples/            runnable configs: hello (no PHP), php-hello (PSR-7 worker), php-kv (workers sharing kv)
docs/                user documentation: getting started, config reference, metrics, operations, benchmarks
test/e2e/            end-to-end tests against the real binary; testdata/symfony is the framework smoke test
test/docs/           docs drift checks: the docs against the code and the Makefile
test/bench/          the benchmark against RoadRunner: configs, a load generator, codec cost in PHP, run.sh
test/docker/         image for the PHP version matrix
```

## Rules in brief

Full list: `.ai/rules.md`. The ones that matter most:

- **Language.** Every artifact (code, comments, docs, commit messages, PR descriptions) is in English.
  Talk to the user in Russian.
- **No AI attribution.** Never add AI attribution anywhere: no `Co-Authored-By:` trailers, no
  "Generated with …" lines, no mentions of an AI agent in code, docs, commits or PRs. This overrides
  any default of your tooling.
- **Go 1.27, stdlib first, pure Go.** No CGO by default. Every new third-party dependency needs a
  stated reason and the user's OK (ADR-0005).
- **Lifecycle discipline.** Every goroutine has an owner, a stop signal and is awaited on shutdown.
  Anything that blocks or does I/O takes `context.Context` as its first parameter.
- **Bounded everything.** Queues, timeouts, body sizes, worker memory — no unbounded resource.
- **Errors.** Wrap with `%w` and context; handle or return, never both. Logging via `log/slog`, logger injected.
- **Config.** Strict decoding (unknown fields are errors), documented defaults, validation errors
  carry the YAML path: `listeners[0].http.virtual_hosts[0].routes[2].route.cluster: unknown cluster "foo"`.
- **Tests.** `go test -race ./...` passes before you say "done". Time-based logic is tested with
  `testing/synctest`, not sleeps. Tests that need PHP skip cleanly when `php` is missing.
- **Prior art is not memory.** RoadRunner/Envoy behavior noted as ❓ in `.ai/research/` is unverified:
  check the upstream source before implementing compatibility.
- **Ask first** before: adding dependencies, changing the config schema, changing an ADR status,
  deleting files, committing (see Git below). Never push or rewrite history unless asked.

## Git

Full workflow: `.ai/rules.md` § 9.

- Remote: `origin` → `git@github.com:berrymore/kest.git` (public). Default branch: `main`.
- **One branch per milestone** from `main`: `feat/m<N>-<slug>`; outside a milestone, one per backlog item:
  `feat/k<NNN>-<slug>`. Nothing is committed to `main` directly.
- **One commit per backlog item** (`K-NNN` in the subject), plus separate `docs:` commits for plans and status.
- **Ask before every commit**, unless the user said otherwise for the current milestone. `make check` first.
- **Merge via PR:** at the end of the milestone, run the docs review (`.ai/review.md`: `make docs-check` plus the
  `docs-reviewer` agent) and fix what it finds, ask, push the branch, `gh pr create`; after the user's review and
  go-ahead (and another docs review if the branch changed), `gh pr merge --squash --delete-branch`.
- Conventional Commits (`feat(config): …`, `fix(worker): …`, `docs: …`).
- No CI yet: the user develops alone and locally. Run the checks from the Commands table yourself
  before declaring work done.

## Map of `.ai/`

| Path | Purpose |
|---|---|
| `.ai/status.md` | Where we are now: phase, in-progress, next steps, open questions, session log |
| `.ai/context.md` | Vision, goals, non-goals, principles, glossary |
| `.ai/rules.md` | Engineering rules and workflow conventions |
| `.ai/architecture.md` | Living architecture: components, config model, interfaces, lifecycle |
| `.ai/roadmap.md` | Milestones M0…M10 with exit criteria; definition of 1.0 |
| `.ai/backlog.md` | All known work items (`K-NNN`), grouped by milestone |
| `.ai/plans/` | Step-by-step plans for the active milestone/feature |
| `.ai/decisions/` | Architecture Decision Records (`NNNN-title.md`) |
| `.ai/research/` | Notes on prior art: RoadRunner, Envoy, protocols |
| `.ai/review.md` | Milestone docs review: when, how, checklist (reviewer: `.claude/agents/docs-reviewer.md`) |

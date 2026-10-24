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

Docs that drift from the code are worse than no docs. Keep them true, short and current.

## Commands

Only build/test/vet work today. Once the Makefile lands (K-001), keep this table in sync with it.

| Task | Command |
|---|---|
| Build | `go build ./...` |
| Test (unit) | `go test -race ./...` |
| Test (integration, needs `php` in PATH) | `go test -race -tags integration ./...` |
| Vet | `go vet ./...` |
| Lint | `golangci-lint run` (config arrives with K-002; not installed locally yet) |
| Format | `gofmt -w .` |
| Run | `go run ./cmd/kest serve -c examples/hello/kest.yaml` (after M0; today only a placeholder `main.go`) |

## Repository layout

Current: `go.mod` (module `github.com/berrymore/kest`, Go 1.27), placeholder `main.go`, docs.
Target layout is described in `.ai/architecture.md` §7 — follow it when adding packages.

```
AGENTS.md        this file (CLAUDE.md imports it)
README.md        user-facing overview
.ai/             agent workspace: status, rules, architecture, roadmap, backlog, plans, ADRs, research
cmd/kest/        CLI entrypoint                    (planned, M0)
internal/        all implementation packages       (planned, M0+)
examples/        runnable configs and PHP apps     (planned)
docs/            user documentation                (planned)
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
  deleting files, committing. Never push or rewrite history unless asked.

## Git

- Remote: `origin` → `git@github.com:berrymore/kest.git` (public). Default branch: `main`.
- Conventional Commits (`feat(config): …`, `fix(worker): …`, `docs: …`), small and coherent.
- No CI yet: the user develops alone and locally. Run the checks from the Commands table yourself
  before declaring work done.

## Map of `.ai/`

| Path | Purpose |
|---|---|
| `.ai/status.md` | Where we are now: phase, in-progress, next steps, open questions, session log |
| `.ai/context.md` | Vision, goals, non-goals, principles, glossary |
| `.ai/rules.md` | Engineering rules and workflow conventions |
| `.ai/architecture.md` | Living architecture: components, config model, interfaces, lifecycle |
| `.ai/roadmap.md` | Milestones M0…M6 with exit criteria; definition of 1.0 |
| `.ai/backlog.md` | All known work items (`K-NNN`), grouped by milestone |
| `.ai/plans/` | Step-by-step plans for the active milestone/feature |
| `.ai/decisions/` | Architecture Decision Records (`NNNN-title.md`) |
| `.ai/research/` | Notes on prior art: RoadRunner, Envoy, protocols |

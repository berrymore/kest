# ADR-0005: Go toolchain and dependency policy

- **Status:** Proposed
- **Date:** 2026-10-24
- **Deciders:** user, agent

## Context

kest aims at production quality and long-term maintainability. Every dependency is a supply-chain,
upgrade and licensing liability. The user develops with Go 1.27 locally.

## Decision

1. **Toolchain:** `go 1.27` in `go.mod`. Bumping the minimum Go version is a deliberate change, noted
   in `status.md`.
2. **Stdlib first:** `net/http`, `log/slog`, `flag`, `os/exec`, `context`, `testing/synctest` cover most needs.
3. **Pure Go by default:** no CGO in the default build, so static binaries and easy cross-compilation keep working.
4. **Licenses:** MIT, BSD or Apache-2.0 only.
5. **Adding a dependency** requires: a reason the stdlib isn't enough, a maintenance check, a license
   check, and the user's OK. Then it is added to the table below.

### Approved dependencies

| Module | Purpose | Status |
|---|---|---|
| YAML library — `go.yaml.in/yaml/v3`/`v4` or `github.com/goccy/go-yaml` | Config parsing with strict mode and node positions | To be chosen in K-005 |
| `golang.org/x/sync` | `errgroup` | Approved if needed |
| `go.uber.org/goleak` | Goroutine leak checks (tests only) | Proposed |
| `github.com/tetratelabs/wazero` | WASM runtime (M5) | See ADR-0006 |
| Metrics library | M3 | To be decided (Q-E) |

## Alternatives considered

- **Framework-style stacks** (cobra/viper, zap, fx) — convenient, but each pulls in a large dependency
  tree, and the stdlib now covers CLI, logging and structured config needs well enough.

## Consequences

- A small, auditable `go.sum`; easy upgrades.
- Some convenience code (CLI subcommands, size parsing) is written by hand.
- Follow-ups: K-005.

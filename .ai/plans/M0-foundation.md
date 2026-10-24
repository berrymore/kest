# Plan: M0 — Foundation

- **Status:** Ready
- **Milestone:** M0
- **Backlog:** K-001…K-015

## Goal

`kest serve -c examples/hello/kest.yaml` starts, answers `curl localhost:8080/` with a configured
`direct_response`, exposes `/ready` on the admin port, and shuts down gracefully on SIGTERM.
`kest validate` rejects invalid configs with precise, path-aware errors. `make check` is green.

## Design notes

- Config model: ADR-0002. Registry: ADR-0004. Dependencies: ADR-0005.
- Minimal M0 config surface:

  ```yaml
  version: v1
  log: { level: info, format: text }
  admin: { address: 127.0.0.1:9901 }
  server: { drain_timeout: 15s }
  listeners:
    - name: main
      address: 127.0.0.1:8080
      http:
        virtual_hosts:
          - name: default
            domains: ["*"]
            routes:
              - match: { path: /ping }
                direct_response: { status: 200, body: "pong\n" }
              - match: { prefix: / }
                direct_response: { status: 200, body: "hello from kest\n" }
  clusters: []
  ```

- **Loader pipeline:** read file → parse to YAML node tree → expand `${VAR}` / `${VAR:-default}` in scalar
  nodes → strict decode into structs → apply defaults → `Validate()` with path accumulation →
  cross-reference checks.
- **Errors:** collect all validation errors (not just the first one) and print them sorted by path,
  with line numbers when the YAML library provides them.
- **CLI:** stdlib `flag` with a small subcommand dispatcher; `version` prints the version, commit and
  build date injected through `-ldflags`.
- **Lifecycle:** `server.Run(ctx, cfg)` builds the graph, starts components in order, waits for the context
  to be cancelled (from `signal.NotifyContext`), then stops in reverse order within `drain_timeout`.
- **Router:** virtual host selection: exact domain → longest wildcard suffix → `*`. Routes are matched in
  order; the first match wins. Return 404 when nothing matches.
- **Even in M0, the registry has two categories** (`http_filter`, `cluster`), so the decoding path for
  typed extensions is exercised. Clusters stay empty until M1/M2, but the category and validation exist.

## Steps

1. [ ] **Skeleton** (K-001) — `cmd/kest/main.go`, package dirs, Makefile, `.editorconfig`.
   Acceptance: `make build test` works; the placeholder `main.go` is gone.
2. [ ] **YAML library choice** (K-005) — short comparison in ADR-0005, user OK.
   Acceptance: the dependency is added and the ADR table updated.
3. [ ] **Config loader** (K-006) — types, `Duration`, `ByteSize`, env expansion, strict decode, defaults,
   validation. Acceptance: table tests for valid and invalid configs (unknown field, bad duration, duplicate
   names, missing version, bad route action); fuzz test for env expansion.
4. [ ] **Registry** (K-007) — generic categories, factories, typed config decoding.
   Acceptance: unit tests for registration, duplicates and decoding errors with paths.
5. [ ] **Logging** (K-009) — slog from config. Acceptance: text and JSON outputs tested.
6. [ ] **HTTP listener + router + direct_response** (K-011). Acceptance: `httptest`-based tests for vhost
   selection, match order, 404, direct responses.
7. [ ] **Admin server** (K-010) — `/ready` (503 → 200 → 503 during drain), `/server_info`.
8. [ ] **Server lifecycle + CLI** (K-008, K-004) — ordered start/stop, signals, drain, `serve`/`validate`/`version`.
   Acceptance: a test that starts the server, sends requests, cancels the context and checks drain and leaks.
9. [ ] **Example + e2e** (K-012) — `examples/hello/kest.yaml`; an e2e test builds the binary, runs it,
   curls it and sends SIGTERM.
10. [ ] **Lint + `make check`** (K-002, K-015) — golangci-lint v2 config; one target runs build, vet,
    lint and `test -race`. No CI yet (K-003 deferred).
11. [ ] **Docs** (K-013) — `docs/config.md` for the M0 surface; update README/AGENTS commands and
    `architecture.md` markers (⚪ → 🟢).

## Risks / open questions

- YAML library line numbers: if strict decoding loses positions, validation errors may only carry paths.
  Acceptable for M0.

## Log

- 2026-10-24 — Plan drafted. Typed extension syntax settled as `{type, config}`; CI replaced by `make check`.

# ADR-0004: Compile-time extension registry with explicit lifecycle

- **Status:** Proposed
- **Date:** 2026-10-24
- **Deciders:** user, agent

## Context

kest needs many pluggable component types: HTTP filters, route actions, cluster types (php_workers, wasm,
http_proxy), later listener protocols. Prior art:

- **Envoy:** extensions are compiled in and registered as named factories that receive typed config.
- **Caddy:** modules register in `init()` by ID; config is decoded into the module's struct.
- **RoadRunner:** plugins are wired by the Endure DI container, with dependencies resolved by reflection.
- **Go `plugin` package:** runtime-loaded `.so` files; fragile (exact toolchain and dependency match
  required) and limited on platforms.

## Decision

1. Extensions are **compiled into the binary** and register factories in `init()` into typed categories
   (`registry.HTTPFilters`, `registry.Clusters`, …).
2. A factory has a `Type` name, a `NewConfig()` that returns a pointer to its config struct (decoded
   strictly), and a `Build(BuildContext, cfg)` that constructs the component **without side effects**
   (no goroutines, no I/O).
3. Dependencies between components (e.g. route → cluster) are resolved **explicitly** by the server
   through lookups in `BuildContext`, not by a DI container or reflection.
4. Components with a lifecycle implement `Start(ctx) error` / `Stop(ctx) error`; the server orders them
   (clusters → admin → listeners on start, the reverse on stop).
5. Duplicate type registration panics at init (a programmer error).
6. Whether a public extension API in `pkg/` is offered (custom builds, like `xcaddy`) is decided later, by a new ADR.

## Alternatives considered

- **DI container (Endure-style)** — automatic wiring, but implicit, reflection-heavy and harder to
  debug; the "what depends on what" moves out of sight.
- **Go `plugin`** — dynamic loading, but operationally fragile; ruled out in `context.md`.
- **Hard-coded switch on type names** — simplest, but every new extension touches core code and config
  decoding gets duplicated.

## Consequences

- Adding an extension = one package + one `init()` registration + a blank import in the binary.
- Build-time composition; custom builds with third-party extensions are possible later.
- The registry is the one allowed piece of global mutable state (frozen after startup).
- Follow-ups: K-007.

# ADR-0006: Use wazero as the WASM runtime

- **Status:** Proposed (decision needed before M5)
- **Date:** 2026-10-24
- **Deciders:** user, agent

## Context

The second big feature after PHP is a WASM sandbox: running small, isolated modules very fast inside
kest, as handlers, HTTP filters, or functions called from PHP. Requirements: strong isolation, per-call
memory and time limits, low per-call overhead (instance pooling, precompilation), and compatibility with
ADR-0005 (pure Go, no CGO).

## Decision

We will use **wazero** (`github.com/tetratelabs/wazero`, Apache-2.0): a pure-Go runtime with zero
dependencies, an ahead-of-time compiler on amd64/arm64 (interpreter elsewhere), a compilation cache,
WASI preview 1, and cancellation of running code through context.

The **guest ABI** (how a module receives a request and returns a response) is **not** decided here.
Candidates: Proxy-Wasm (Envoy ecosystem), http-wasm, WASI stdin/stdout (CGI-style), a custom ABI.
It will get its own ADR (K-100).

## Alternatives considered

- **wasmtime-go** — the most feature-complete runtime (component model, WASI preview 2), but it needs
  CGO and a native library.
- **wasmer-go** — CGO, and development appears stalled ❓.
- **Running WASM out of process** — strongest isolation, but loses the "lightning-fast in-process" property.

## Consequences

- No CGO; the single static binary stays.
- No component model / WASI preview 2 in wazero ❓ — limits the ABI choice; check at M5 start.
- Follow-ups: K-100…K-104. Re-verify the wazero feature set (❓ items) when M5 starts.

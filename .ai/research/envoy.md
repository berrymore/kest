# Envoy — prior art notes

Legend: ✅ verified (with a source link) · ❓ unverified (from memory).
Envoy is the inspiration for the *shape* of kest's configuration and operations, not a compatibility target.

Sources: https://www.envoyproxy.io/docs, https://github.com/envoyproxy/envoy (C++, Apache-2.0).

## Configuration model ❓

- **Bootstrap:** `admin`, `static_resources` (`listeners`, `clusters`), `dynamic_resources` (xDS: LDS, CDS,
  RDS, EDS, SDS…).
- **Listener** → `filter_chains` (selected by `filter_chain_match`: SNI, ALPN, source IP…) → network
  `filters`, typically `envoy.filters.network.http_connection_manager`.
- **HTTP connection manager** → `http_filters` (ordered, ending with `envoy.filters.http.router`) and
  `route_config` → `virtual_hosts` (selected by `domains`) → `routes` (`match` + one action: `route`,
  `redirect`, `direct_response`).
- **Cluster** → discovery type (static, strict/logical DNS, EDS) → `load_assignment` endpoints; LB policy,
  health checks, circuit breakers, outlier detection, timeouts, retries.
- **Typed extensions:** `name` + `typed_config` with an `@type` URL pointing to a protobuf message.

## What kest borrows

| Envoy | kest |
|---|---|
| listeners / clusters at the top level | same (`listeners`, `clusters`) |
| HTTP connection manager with ordered HTTP filters | `listeners[].http.filters`; router implicit |
| virtual hosts by `domains`, ordered routes, first match wins | same |
| route actions: `route`, `direct_response`, `redirect` | same names |
| per-route timeout | `route.timeout` |
| typed extension config | `{type, config}` without `@type` URLs (ADR-0002) |
| admin interface: `/ready`, `/config_dump`, `/clusters`, `/stats`, `/server_info` | admin server (architecture.md §8) |
| graceful drain, hot restart | drain on shutdown (M0); hot restart in the icebox |
| overload manager (shed load under pressure) | bounded queues + fast 503 when a pool is saturated |
| Proxy-Wasm filters | candidate ABI for `wasm` filters (K-100) |

## What kest does not borrow (for now)

- Protobuf-first config and `@type` URLs.
- xDS dynamic config (M6 may take ideas, not the wire protocol).
- Network (L4) filter chains and `filter_chain_match`; kest starts with HTTP listeners only.
- Threading model details (Envoy is event-loop per worker thread; kest uses Go's `net/http` + goroutines).

## Questions for later

- Envoy's drain sequence (`drain_timeout`, `--drain-strategy`) — details to mirror in M3/M6.
- Retry and timeout semantics for `http_proxy` clusters (M4).
- How the Proxy-Wasm ABI maps onto wazero, and the existing Go host implementations (M5).

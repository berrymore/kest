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

## Route matching ✅ (checked 2026-10-25 for M5)

Source: [`api/envoy/config/route/v3/route_components.proto`](https://github.com/envoyproxy/envoy/blob/86af01fdec40c31d025c495a4aa303c4126b2fd0/api/envoy/config/route/v3/route_components.proto)
at v1.40.1.

- `RouteMatch.path_specifier`: `prefix`, `path` (exact, query removed), `safe_regex` (RE2; the **whole** path
  without the query must match), `path_separated_prefix` (`/api` matches `/api`, `/api/…`, not `/apix`),
  `connect_matcher`, `path_match_policy` (extension). `case_sensitive` (default true, ignored for regexes).
- `headers`: all must match. `HeaderMatcher`: `name` plus one of `string_match` (`StringMatcher`: `exact`, `prefix`,
  `suffix`, `contains`, `safe_regex`, with `ignore_case`), `present_match`, `range_match`; `invert_match`,
  `treat_missing_header_as_empty`. The older `exact_match`, `safe_regex_match`, … are deprecated. Methods are matched
  as the `:method` pseudo-header; there is no `methods` field.
- `query_parameters`: all must match; `QueryParameterMatcher`: `name` plus `string_match` or `present_match`; a
  repeated parameter is matched by its first value.
- Also `runtime_fraction`, `grpc`, `tls_context`, dynamic metadata and filter state matchers.

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
- xDS dynamic config (M8 may take ideas, not the wire protocol).
- Network (L4) filter chains and `filter_chain_match`; kest starts with HTTP listeners only.
- Threading model details (Envoy is event-loop per worker thread; kest uses Go's `net/http` + goroutines).

## Questions for later

- Envoy's drain sequence (`drain_timeout`, `--drain-strategy`) — details to mirror in M3/M8.
- The names borrowed from Envoy (`normalize_path`, `merge_slashes`, `prefix_rewrite`) are from memory ❓ and
  describe kest's behavior only.
- How the Proxy-Wasm ABI maps onto wazero, and the existing Go host implementations (M6).

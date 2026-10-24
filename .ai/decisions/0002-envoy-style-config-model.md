# ADR-0002: Envoy-style declarative configuration model

- **Status:** Accepted (2026-10-24)
- **Date:** 2026-10-24
- **Deciders:** user, agent

## Context

The user wants kest to be configured flexibly in YAML, "like Envoy". Envoy's model (listeners → filter
chains → HTTP connection manager → HTTP filters → route config → virtual hosts → routes → clusters) is
proven and explicit, but its protobuf-derived YAML is verbose (`typed_config`, `@type` URLs, deep nesting).
RoadRunner's `.rr.yaml` is concise but organized per plugin, and it doesn't express a routing graph
across several pools.

## Decision

We will use a single YAML file with an Envoy-like graph and a lighter syntax:

1. **Top-level sections:** `version` (mandatory, `v1`), `log`, `admin`, `server` (process-wide settings such
   as `drain_timeout`), `listeners`, `clusters`.
2. **Listeners** have a `name`, an `address` and a protocol block (`http:` first). The HTTP block holds
   `filters` and `virtual_hosts`.
3. **The router is implicit** and always runs last; `filters` lists only non-terminal filters.
   (Envoy requires an explicit `envoy.filters.http.router`; kest does not.)
4. **Virtual hosts** select by `domains` (exact, `*.suffix`, `*`). **Routes** have a `match` (`prefix`,
   `path`; later `regex`, `headers`, `methods`) and exactly one action: `route: {cluster, timeout, …}`
   or `direct_response: {status, body}` (later `redirect`).
5. **Clusters** have a `name`, a `type` and a `config`.
6. **Typed extensions** everywhere use `{type: <short_name>, config: {...}}`. The type name is resolved
   in the registry of the category where it appears (HTTP filter, cluster, …), so names are short
   (`access_log`, `php_workers`) and need no namespacing.
7. **Strict decoding:** unknown fields are errors at every level, including inside `config`.
8. **Validation before side effects:** schema, value ranges and cross-references (route → cluster,
   unique names) are all checked before any port is bound or process spawned.
9. **Env expansion** (`${VAR}`, `${VAR:-default}`) is applied to scalar values after YAML parsing.
10. **Effective config** (with defaults) can be dumped (`kest validate --dump`, admin `/config_dump`).

## Alternatives considered

- **Envoy's syntax verbatim** (`typed_config`, `@type`) — familiar to Envoy users, but too noisy for a PHP
  app server and tied to protobuf.
- **Inline typed config** (`{type: cors, allow_origins: [...]}`) — less nesting, but strict decoding has
  to split the `type` key from extension fields, and error paths get muddier. The user chose
  `{type, config}` over this on 2026-10-24.
- **Single-key maps** (`- cors: {...}`) — compact, but awkward to validate and to order.
- **RoadRunner-like per-plugin sections** — concise, but can't express several listeners and pools with
  routing between them.

## Consequences

- One mental model for every extension point; easy to add new filter and cluster types.
- Some verbosity (`config:` nesting) in exchange for unambiguous strict decoding.
- No `.rr.yaml` compatibility; a migration guide is needed later.
- Follow-ups: K-005 (YAML library), K-006 (loader), K-007 (registry), K-013 (config reference).

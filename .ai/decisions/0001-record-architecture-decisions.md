# ADR-0001: Record architecture decisions

- **Status:** Accepted
- **Date:** 2026-10-24
- **Deciders:** user, agent

## Context

kest is developed by a human together with AI agents across many sessions. Agents don't remember previous
sessions, so decisions and their reasoning must live in the repository. Without that, they get
re-litigated or silently contradicted.

## Decision

We will record significant decisions as ADRs in `.ai/decisions/NNNN-short-title.md`, using
`0000-template.md`.

- A decision is significant if it is expensive to reverse, or if it affects the config schema, the
  public behavior, dependencies, the process model, protocols or cross-cutting code conventions.
- Agents create ADRs with status `Proposed`. Only the user moves them to `Accepted` or `Rejected`.
- Accepted ADRs are not rewritten. A change of mind gets a new ADR that supersedes the old one, and the
  old one's status is updated to point to it.
- `architecture.md` describes the current design and links the ADRs behind it.

## Consequences

- New sessions can recover the "why" quickly.
- Writing ADRs costs a little time; keep them short (one screen is ideal).

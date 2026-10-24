# `.ai/` — project workspace for agents

This directory is the project's long-term memory. Chat history is lost between sessions; this is not.
It is written for AI agents first, but every file should make sense to a human.

## Map

| File | What it holds | Read | Update |
|---|---|---|---|
| `status.md` | Current phase, in-progress work, next steps, open questions, environment, session log | Every session start | Every session end |
| `context.md` | Vision, goals, non-goals, principles, glossary | First session; when scope is unclear | Only with the user, when the vision changes |
| `rules.md` | Engineering rules and workflow conventions | Before writing code | When the user sets a durable rule |
| `architecture.md` | Living description of the system: components, config model, interfaces | Before touching a subsystem | In the same change that alters the architecture |
| `roadmap.md` | Milestones with scope and exit criteria; definition of 1.0 | When planning | When a milestone closes or priorities change |
| `backlog.md` | Every known work item, grouped by milestone | When picking work | Continuously |
| `plans/` | Detailed, step-by-step plans for the active milestone or a large feature | While implementing | As steps complete |
| `decisions/` | ADRs: context, decision, consequences; index in `decisions/README.md` | Before changing anything an ADR covers | When a significant decision is made |
| `research/` | Notes on prior art (RoadRunner, Envoy, protocols, runtimes) | Before implementing compatibility | When something is learned or verified |

## Conventions

- **IDs.** Backlog items `K-001`; ADRs `ADR-0001` (file `decisions/0001-title.md`); milestones `M0`…;
  open questions `Q1`… in `status.md`. IDs are never reused. Backlog ID ranges per milestone
  (K-001…, K-020…, K-040…) are only a convenience.
- **Dates** are absolute: `YYYY-MM-DD`.
- **Backlog states:** `[ ]` todo · `[~]` in progress · `[x]` done · `[-]` dropped (say why).
- **ADR states:** `Proposed` → `Accepted` | `Rejected`; later `Superseded by ADR-NNNN` or `Deprecated`.
  ADRs are not rewritten after acceptance: write a new one that supersedes them.
- **Facts vs. assumptions.** In `research/`, mark claims ✅ verified (with a source link) or ❓ unverified.
  Never implement compatibility against a ❓ claim — verify it first.
- **Brevity.** Prefer updating or deleting stale text over appending. The session log in `status.md`
  keeps roughly the last 15 entries; older history lives in git.

## Who decides what

- **The user decides:** vision and scope, ADR acceptance, config schema changes, new dependencies,
  milestone priorities, license, anything public.
- **The agent proposes:** designs, plans, ADR drafts, backlog items, and keeps these files accurate.

## Session protocol

**Start**
1. `status.md` → where we are and what's next.
2. The active plan in `plans/` (linked from `status.md`).
3. Relevant `architecture.md` sections and ADRs for the area you will touch.

**During**
- New idea or problem outside the current task → add it to `backlog.md` instead of expanding scope.
- A decision with lasting consequences → draft an ADR and flag it in `status.md` → Open questions.

**End**
1. `status.md`: update Current phase / In progress / Next up / Open questions; add one session-log line.
2. Tick items in `backlog.md` and the active plan.
3. Make sure `architecture.md` still matches the code.

## Templates

- ADR: `decisions/0000-template.md`
- Plan: `plans/README.md`

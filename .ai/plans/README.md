# Plans

A plan is a step-by-step guide for a milestone or a large feature that takes more than one session.
Small tasks don't need a plan file; the backlog item is enough.

- One file per plan: `M<N>-<slug>.md` for milestones, `<slug>.md` for features.
- `status.md` links the active plan.
- Steps reference backlog IDs. Tick steps as they complete; record deviations from the plan in its log.
- When a plan is finished, set its status to `Done` and keep it as a record (or delete it if it adds nothing beyond git history).

## Template

```markdown
# Plan: <title>

- **Status:** Draft | Active | Done | Abandoned
- **Milestone:** M<N>
- **Backlog:** K-…, K-…

## Goal
One paragraph: what is true when this plan is done.

## Design notes
Key decisions and constraints for this plan (link ADRs).

## Steps
1. [ ] <step> (K-…) — acceptance: <how we know it's done>
2. [ ] …

## Risks / open questions

## Log
- YYYY-MM-DD — …
```

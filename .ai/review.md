# Milestone docs review

Docs drift: `status.md` says a PR is waiting after it was merged, a backlog item stays open after its commit, the
config reference misses an option. This review catches drift before a milestone reaches `main`.

## When

- **Before `gh pr create`** at the end of every milestone.
- **Again before `gh pr merge`** if the branch changed after the review (fixes from the user's review).
- Optionally at a `/clear` handoff in the middle of a milestone: the `status.md` part only.

## How

1. `make check` is green. It includes `make docs-check` (`test/docs`), the mechanical part: links and anchors,
   make targets, backlog and ADR IDs, the ADR index, the package layout, the config reference, tool versions,
   status dates, plans marked Done.
2. A reviewer who didn't write the change goes through the checklist below against `git diff main...HEAD` and
   `git log main..HEAD`, and reports each finding with `file:line`, evidence and a fix. The reviewer reports
   and doesn't edit. In Claude Code this is the `docs-reviewer` agent (`.claude/agents/docs-reviewer.md`); with
   other tools, a fresh session given this file.
3. The author fixes every finding, or says in the PR why not, commits the fixes as `docs: …`, reruns
   `make check`, and states in the PR description that the review ran.

## Checklist

Describe the state **after the merge**: the next session starts from `main`, possibly after `/clear`.

**`status.md`**
- Current phase and In progress say the milestone is done and merged, not "in progress" or "waiting for review".
- Next up lets a fresh session start: the first concrete step, the files to read, the open decisions, the branch
  to create.
- Open questions: the resolved ones moved to Resolved decisions with a date; new ones added.
- One session-log line per session of the milestone; about 15 entries kept.
- Environment is still true (tool versions, Docker images).

**`backlog.md` and the plan**
- Every `K-NNN` in `git log main..HEAD` is `[x]` with its outcome; nothing is `[x]` without a commit or a reason.
- Work found along the way is in the backlog; dropped items are `[-]` with a reason.
- The plan's steps are ticked, its status is `Done` with a date, and its log records decisions and deviations.

**`roadmap.md`**
- The milestone's status marker and scope range are current; each exit criterion is met, or what is left is in
  the backlog.

**`architecture.md` and ADRs**
- Every package, interface, lifecycle rule and error contract changed in the diff is described, with true status
  markers (🟢 🟡 ⚪).
- Significant decisions have an ADR (`Proposed` until the user accepts it); the index matches.

**User docs: `docs/`, `README.md`, `examples/`**
- Every user-visible change in the diff (config option, default, CLI flag, an error message the user acts on,
  behavior) is in `docs/config.md` or `docs/getting-started.md`.
- README's status and feature lines match what works.
- The example configs validate: `./bin/kest validate -c examples/<name>/kest.yaml`.

**`AGENTS.md` and `rules.md`**
- The commands table, the repository layout and the rules in brief are still true.
- Rules the user set during the milestone are in `rules.md`.

**`research/`**
- Claims verified during the milestone are ✅ with a source; nothing was implemented against a ❓ claim.

**Commits**
- One commit per backlog item with `K-NNN` in the subject; Conventional Commits; English.
- No AI attribution in commits, the PR description, code or docs.

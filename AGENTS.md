<!-- template: AGENTS-GITFLOW-AUTO | version: 1.2.1 | source-updated: 2026-07-25 | adapted: 2026-08-05 -->

# AGENTS.md — Weave Scope (alfiyansys fork)

Fork of the now-deprecated `weaveworks/scope` — a Docker/Kubernetes
infrastructure visualization tool (probe agents report topology + metrics,
an app aggregates and serves a live map to a browser client). Upstream is
frozen as of 2023-06-13; this fork exists to modernize it against current
Docker Engine, Docker Swarm, and Kubernetes.

**Read `MODERNIZATION-PLAN.md` first before starting work** — it's the source of truth for
phase order and scope (0: CI baseline → 1: Go toolchain → 2: Docker client →
3: Swarm validation → 4: eBPF → 5: Kubernetes → 6: cleanup); this file is
just the rules that are easy to violate if you only read the code.

## Status

Progress is tracked via checkboxes in `MODERNIZATION-PLAN.md` — trust that over this
file's wording, which can go stale. Cross-session progress notes for this
project also live in the uteke-mcp `work-personal` room — check there for
anything logged in a prior session before assuming a clean slate.

## Building

The backend normally builds **inside a Docker build container**
(`weaveworks/scope-backend-build`, driven by `Makefile`'s
`BUILD_IN_CONTAINER=true`), not with a bare local `go build` — this matters
because Phase 1 of `MODERNIZATION-PLAN.md` is specifically about modernizing that
container image (`tools/build/golang/Dockerfile`, currently
`golang:1.14.4-stretch`).

```bash
make                # full containerized build -> scope.tar
make shell          # drop into the build container for ad-hoc go commands
make client-start   # local UI dev server (client/ — separate Node/React app)
```

Once Phase 1 lands, prefer running `go build ./...` / `go vet ./...` directly
against the host Go toolchain for fast iteration, falling back to the
containerized build to confirm parity before considering a phase done.

## Testing

```bash
make tests          # backend unit tests (containerized)
make lint            # backend lint (containerized)
make client-test    # client/UI unit tests
make client-lint    # client/UI lint
```

No end-to-end test suite exists for the Docker/Swarm/Kubernetes integration
paths this fork cares about most — validation there means running against a
real Engine/Swarm/cluster and checking the UI/API output, per the
"Definition of Done" in each `MODERNIZATION-PLAN.md` phase. Don't claim a phase works from
a green `go test` run alone.

## Architecture

```
client (browser UI)  --4040-->  app (aggregator)  <--4040--  probe (per host: docker/k8s/process/network scanners)
```

- `probe/` — per-host agents: `probe/docker`, `probe/kubernetes`,
  `probe/endpoint` (incl. the eBPF connection tracer), `probe/host`,
  `probe/process`, `probe/awsecs`.
- `app/` — aggregates probe reports, serves the topology API and static UI;
  `app/multitenant/` is Weave-Cloud-era code, not relevant to this fork's goals.
- `render/` — turns raw `report.Report` data into renderable topologies
  (`render/swarm.go` is the Swarm-specific renderer).
- `report/` — the core data model (flat, multi-topology — see the original
  2014 design notes in git history if the "why flat, not nested" question
  comes up).
- `client/` — the browser UI (separate Node/React toolchain, `client/app`).

## Start Simple, Don't Overdo It

Build the smallest change that satisfies the current `MODERNIZATION-PLAN.md` phase. If a
phase starts feeling too complex mid-work, stop — break it into smaller
commits, or narrow scope to what's needed for that phase's Definition of
Done and leave the rest for a later phase. If something already built proves
unnecessary, remove it completely: code, the relevant `MODERNIZATION-PLAN.md` checkbox,
and a line in the "Rules Not to Break" section below recording why — so it
isn't quietly rebuilt.

## Framework/Dependency Drift Warning

Nearly every dependency in this repo (`go.mod`, `client/package.json`) is
several years stale by design — that's the whole point of this fork. Do
**not** assume training-data knowledge of `fsouza/go-dockerclient`,
`k8s.io/client-go`, or the Docker Engine API matches what's pinned here
today, or matches what the *target* modern version looks like. Check the
installed/vendored version and the target version's actual docs before
writing code against either one.

## Rules Not to Break Without Discussing First

Some things here look wrong or unfinished if you only read the code —
they're deliberate. Before "fixing" one, re-read the reasoning and check
with the user first.

<!-- Populate as decisions get made:
- **`<file/behavior>`.** `<what looks off>`. Reason: `<why it's deliberate>`.
  Don't `<the "obvious fix" to avoid>` without checking first.
-->

## Code Conventions

- Backend: Go, `gofmt`-clean, no code comments unless requested by the user
  or the WHY is genuinely non-obvious (see global convention).
- Client: existing `client/app/scripts` JS/React style — match it, don't
  introduce a new pattern mid-file.
- Read files before editing; don't guess at struct/API shapes for the
  dependencies named in `MODERNIZATION-PLAN.md` — verify against the actual vendored or
  target version.

## Secrets

No `.env`/credential files are expected at the repo root today. If AWS
credentials are needed for `probe/awsecs` work, they come from the
environment via standard AWS SDK resolution — never hardcode a key/secret
in source, tests, or commit messages.

## Git Workflow — Branching

- `master` — protected. Never commit directly; only
  `git merge --no-ff dev` after review/smoke-test against a real
  Docker/Swarm/Kubernetes target per the relevant phase's Definition of Done.
- `dev` — default branch for this modernization effort, create it if it
  doesn't exist yet; all phase work lands here first.
- `phase-<n>-<slug>` (e.g. `phase-2-docker-client`) — branch off `dev` for
  work spanning multiple sessions; merge back into `dev` with `--no-ff`,
  then delete.

Always branch off `dev`, never `master`.

## Commit Convention — Conventional Commits, Granular

```
<type>(<scope>): <short, present-tense, why-focused summary>
```

Types: `feat`, `fix`, `refactor`, `docs`, `chore`, `test`, `style`.

**One commit = one logically self-contained change** — don't mix behavior
with formatting, refactors with features, or code with unrelated docs. Each
commit should leave the project buildable. No empty commits, no generic
messages. Never amend or rewrite pushed history unless asked.

**No `Claude-Session:` trailer.** Commits may keep `Co-Authored-By: Claude
Sonnet 5 <noreply@anthropic.com>`, but never add a `Claude-Session: <url>`
line — omit it from the commit template entirely, don't just remember to
strip it after the fact. The full existing history on `master` and `dev`
had every `Claude-Session:` trailer removed via `git filter-repo` on
2026-08-05 (rewrote every commit from `ff765b5b` onward, force-pushed both
branches) — don't reintroduce what was deliberately removed.

## Auto-Commit Authorization

The agent may commit and push **without asking each time**, within this
boundary:

- Only after a change is a complete logical unit (per above), and only on
  the `dev` branch.
- `git commit` + `git push origin dev` only — never `master`, `--force`,
  `reset --hard`/`rebase`/`commit --amend` on pushed history, or a remote
  other than `origin`.
- Never commit anything that might contain secrets or AWS credentials —
  always needs manual review.

Merging `dev` → `master` always needs explicit confirmation. If a change
isn't clearly a complete logical unit — e.g. mid-way through validating a
phase against a real Engine/cluster — don't auto-commit; ask, or wait.

## Before Each Commit — Checklist

- [ ] Does the project still build (`make` or `go build ./...`)?
- [ ] For Docker/Swarm/Kubernetes phase work: was it actually exercised
      against a real target, not just compiled?
- [ ] Commit message in the required format, touches only related files?
- [ ] No leftover debug prints / commented-out code?
- [ ] `MODERNIZATION-PLAN.md` checkboxes updated to match what actually landed?

## Communication

When asked to "continue": check `MODERNIZATION-PLAN.md` for the next unchecked task in the
current phase, execute it in one or more commits, report what was done and
what's next. Log meaningful progress to the uteke-mcp `work-personal` room
before wrapping up (established convention for this project — see project
memory). When stuck: read existing code for the pattern first; if still
unclear, ask rather than guess.

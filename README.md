# Scope: a modernized fork of Weave Scope

Weave Scope automatically generates a map of your application: probe agents
running on each host report Docker/Kubernetes/process/network topology and
metrics to a central app, which merges them into a live, interactive map in
your browser. You can drill into a single container or process, see its
metrics, and pause/stop/restart/exec into it without leaving the page.

Upstream [`weaveworks/scope`](https://github.com/weaveworks/scope) has been
unmaintained since its last real commit on 2023-06-13, and its dependency
tree (Go 1.16, a 2018-era Docker client, `k8s.io/client-go` v10,
`k8s.io/kubernetes` v1.13) is frozen at roughly 2018–2020 vintage. **This
fork exists to bring the same tool back to working order against current
Docker Engine, Docker Swarm, and Kubernetes**, without changing what Scope
actually does.

**This is a personal fork of an unmaintained project, not an official or
community-governed successor.** Same situation described in
[weaveworks/scope#3921](https://github.com/weaveworks/scope/issues/3921):
there's no roadmap and no commitment to maintain this long-term; it's one
person's fork, published in case it's useful to anyone else still running
Scope. No affiliation with Weaveworks. Bug reports are welcome; fixes
aren't guaranteed.

The original upstream README, as archived at the point this fork started,
is kept at [`README-original.md`](README-original.md).

## What it looks like

<img src="imgs/topology.png" width="200" alt="Map your architecture" align="right">

Choose an overview of your container infrastructure, or focus on a specific
microservice. Easily identify and correct issues to ensure the stability and
performance of your containerized applications.

<img src="imgs/selected.png" width="200" alt="Focus on a single container" align="right">

View contextual metrics, tags, and metadata for your containers. Navigate
between processes inside a container and the hosts they run on, in
expandable, sortable tables. Find the container using the most CPU or
memory for a given host or service.

<img src="imgs/terminals.png" width="200" alt="Launch a command line" align="right">

Interact with containers directly: pause, restart, and stop them, or launch
a shell into one, all without leaving the browser.

<br clear="right">

## Status

Tracked in detail, phase by phase, in [`MODERNIZATION-PLAN.md`](MODERNIZATION-PLAN.md); that
file (not this README) is the source of truth for what's done, what's
in progress, and why. Summary as of this fork's most recent work:

| Phase | Scope | State |
|---|---|---|
| 0 | Baseline build/vet, regression reference | done (CI automation itself deferred; local-only for now) |
| 1 | Go toolchain (1.16 → current), build image, `vendor/` | done |
| 2 | Official `docker/docker/client` SDK, drop `fsouza/go-dockerclient` | done: validated live against a real Engine (27.2.0 / API 1.47) |
| 3 | Docker Swarm mode validation | done: validated live on a real 4-node Swarm cluster |
| 4 | eBPF endpoint tracking | done: confirmed working unmodified on current 5.x/6.x/7.x kernels; the real gap was a missing `/sys/kernel/debug` mount, not the tracer itself |
| 5 | Kubernetes client (`client-go` v10 → v0.36.3, drop `k8s.io/kubernetes`) | mostly done: compiles and unit-tests clean; live-cluster validation still needs a real `kubeadm`/`kind`/EKS target |
| 6 | Base image, Weave Net integration decision, `govulncheck` | not started |

This fork is also running as real, standing infrastructure on a 4-node
Docker Swarm cluster; see [`DEPLOYMENT-PLAN.md`](DEPLOYMENT-PLAN.md) for
what's deployed where, how images get distributed (GHCR, no more manual
`docker save`/`load`), and the operational gotchas found along the way
(Swarm services can never get `--net=host`, kprobes are host-global, etc).

Along the way this effort also found and fixed several real, previously
invisible bugs unrelated to any dependency bump, e.g. an infinite-recursion
codec bug that crashed 3 packages, a missing generated file that silently
corrupted the live JSON API and left every topology view empty in the
browser, and a `SwarmService`/`ECSService`/`ECSTask` node-tagging bug that
made the entire "Services" tab disappear even though the underlying data
was correct. Details and root causes are in `MODERNIZATION-PLAN.md`'s
per-phase write-ups, not repeated here.

The client (browser UI) got a dark mode toggle (2026-08-06): a Redux
`darkMode` flag flows through a `ThemeProvider` covering the node-details
panel, help panel, overlays, and footer. Checking it live the next day
turned up two bugs that only show up once dark mode is actually used
against real data: selecting a node washed the *entire* page — including
the top nav bar — light gray, because the full-canvas selection overlay
read a hardcoded light color instead of the app's theme; and every
topology-graph node rendered as a stark white box regardless of theme,
because the node shapes and label plates come from a pinned third-party
dependency (`weaveworks-ui-components`) that hardcodes white fills with no
theme hook at all. Fixed the former in-repo, and the latter via a
`patch-package` patch (`client/patches/weaveworks-ui-components+0.22.8.patch`)
threading a `darkMode` prop through the same path the existing
`contrastMode` accessibility mode already uses. Both fixes are live on the
production deployment.

## Architecture

```
client (browser UI)  --4040-->  app (aggregator)  <--4040--  probe (per host: docker/k8s/process/network scanners)
```

- `probe/` - per-host agents: `probe/docker`, `probe/kubernetes`,
  `probe/endpoint` (incl. the eBPF connection tracer), `probe/host`,
  `probe/process`, `probe/awsecs`.
- `app/`: aggregates probe reports, serves the topology API and static UI.
- `render/`: turns raw `report.Report` data into renderable topologies
  (`render/swarm.go` is the Swarm-specific renderer).
- `report/`: the core data model (flat, multi-topology).
- `client/`: the browser UI (separate Node/React toolchain under `client/app`;
  still on an old toolchain, not yet in scope of this modernization pass).

## Building

The backend normally builds **inside a Docker build container**
(`weaveworks/scope-backend-build`, driven by `Makefile`'s
`BUILD_IN_CONTAINER=true`); Phase 1 modernized that image
(`tools/build/golang/Dockerfile`) to `golang:1.22-bookworm`.

```bash
make                # full containerized build -> scope.tar
make shell          # drop into the build container for ad-hoc go commands
make client-start   # local UI dev server (client/, a separate Node/React app)
```

With Phase 1 landed, day-to-day iteration works directly against the host
Go toolchain too (`go build ./...` / `go vet ./...`), falling back to the
containerized build to confirm parity before considering a phase done.

## Testing

```bash
make tests          # backend unit tests (containerized)
make lint           # backend lint (containerized)
make client-test    # client/UI unit tests
make client-lint    # client/UI lint
```

There's no end-to-end suite for the Docker/Swarm/Kubernetes paths this fork
cares about most: validating those means running against a real
Engine/Swarm/cluster, per the "Definition of Done" recorded for each phase
in `MODERNIZATION-PLAN.md`. A green `go test ./...` is necessary, not
sufficient, for calling a phase done here.

`integration/swarm-live-validation.sh` is one such check, added in Phase 3:
point it at any Swarm reachable over SSH and it deploys a disposable
service, confirms it shows up correctly in the topology API, and cleans up.

## Running it

The probe and app are one binary (`scope`), split by `--mode`:

```bash
# central app (aggregates + serves the UI/API on :4040)
scope --mode=app

# a probe, reporting Docker containers to that app
scope --mode=probe --probe.docker=true <app-host>:4040
```

For full connection tracking (eBPF, falling back to conntrack/proc
scanning if unavailable) the probe needs host networking, host PID
namespace, and `/sys/kernel/debug` mounted:

```bash
docker run -d --restart=always \
  --net=host --pid=host --privileged \
  --entrypoint /usr/bin/scope \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v /sys/kernel/debug:/sys/kernel/debug \
  ghcr.io/alfiyansys/scope:modernized-8f6b5774 \
  --mode=probe --probe.docker=true --weave=false --no-app <app-host>:4040
```

A pre-built image is published at
[`ghcr.io/alfiyansys/scope`](https://github.com/alfiyansys/scope/pkgs/container/scope)
(public, no login needed to pull); see `DEPLOYMENT-PLAN.md` for the exact
Swarm-service and host-probe layouts this fork actually runs in production,
including why Swarm services alone can't see cross-container traffic and
what the additive host-networked probe does about it.

For Kubernetes and other install paths, upstream's install docs still
describe the underlying mechanism accurately even though the project itself
is archived: <https://www.weave.works/docs/scope/latest/introducing/>.

## Contributing / workflow

This fork's branching model, commit convention, and auto-commit rules are
defined in [`AGENTS.md`](AGENTS.md): `master` is protected, work lands on
`dev` (optionally via a `phase-N-<slug>` branch), Conventional Commits,
one logically self-contained change per commit. Upstream's
[`CONTRIBUTING.md`](CONTRIBUTING.md) is still a useful read for the
project's original conventions; `MODERNIZATION-PLAN.md` documents exactly
where this fork's workflow deliberately diverges from it and why.

## License

Scope is licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for the full license text.
Find more details about the licenses of dependencies (including former vendored code) in [VENDORED_CODE.md](VENDORED_CODE.md).

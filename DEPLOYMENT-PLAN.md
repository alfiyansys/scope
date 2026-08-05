# Scope Fork — Live Deployment Plan (sm-qohelet Swarm)

Separate from `MODERNIZATION-PLAN.md`. That file tracks bringing the
*codebase* up to date (Go toolchain, Docker client, Swarm/K8s/eBPF
support). This file tracks actually **running** this fork as real,
standing infrastructure on the user's Swarm cluster (manager
`sm-qohelet.local`, workers `daya-regia.invis`, `sw-david01`,
`vanguard`) — a different, ongoing concern: deployment topology,
image distribution, and operational follow-ups, not code changes.

Depends on `MODERNIZATION-PLAN.md` Phases 1–3 being done (Go
toolchain, Docker client, Swarm support) — it is, as of this plan's
creation.

## Status

| Piece | State |
|---|---|
| Central app (`scope_app` on the manager) | **Live** — deployed, verified in browser |
| Per-node probe agents (global service) | Not built |
| Cross-host communication graph | Blocked on the above — Scope can only draw an edge between two hosts if both have a probe reporting; only the manager does right now |

## Stage 1 — Central app on the manager (done)

**What's deployed:** a single Swarm service, `scope_app`, one replica, constrained to `node.hostname==sm-qohelet`, running app+probe together in one container (`docker/entrypoint-deploy.sh`), `/var/run/docker.sock` mounted read-only, port 4040 published. Live at `http://sm-qohelet.local:4040`.

**How it was built:**
- `docker/Dockerfile.deploy` — a pragmatic multi-stage build, deliberately *not* `docker/Dockerfile.scope` (that one chains through `Dockerfile.cloud-agent` → `weaveworks/cloud-agent`, an Alpine/musl static-linking path bundling Weave Net binaries this deployment doesn't need). Builder is `golang:1.25-bookworm` — `go.mod`'s `go` directive got auto-raised to `1.25.0` by a later `go mod tidy` during `MODERNIZATION-PLAN.md` Phase 2/3 that nobody reconciled with Phase 1's docs (still says 1.22 there); `tools/build/golang/Dockerfile` needs the same bump — tracked as a `MODERNIZATION-PLAN.md` follow-up, not here. Runtime is `debian:bookworm-slim`, not Alpine — the binary is CGO-enabled (`gopacket`/gopcap, `tcptracer-bpf` don't build under `CGO_ENABLED=0`), so it links glibc/libpcap/libnl/libdbus, not musl.
- Client UI baked in from what was already built locally for `MODERNIZATION-PLAN.md` Phase 2's browser validation (`client/build`, `client/build-external`, `prog/staticui`, `prog/externalui`) — not rebuilt inside the Docker build. Rebuilding the client toolchain in-container (Node 10.19-era, needs `NODE_OPTIONS=--openssl-legacy-provider`) is real future work.
- Image transferred via `docker save | ssh sm-qohelet.local docker load` — no registry, so this exact image only exists on that one node. Fine for one replica; **won't scale to Stage 2 without a registry.**
- `.dockerignore` added (excludes `.git`, `client/node_modules`, ~340MB) so the build context doesn't ship irrelevant files.

**Known limitation found live:** with only the manager's probe running, the UI shows only the manager's own containers/processes — no cross-host communication graph to `sw-david01` or `vanguard`, since Scope needs a probe on *both* ends of a connection to resolve it into a real edge instead of leaving one side unresolved. Not a bug — this is exactly what Stage 2 exists to fix.

## Stage 2 — Global per-node probe agent (not built)

**Why:** Scope's cross-host correlation is existing, already-built behavior (the probe/app split is the original design, going back to 2015 — see `MODERNIZATION-PLAN.md`'s Phase 0 history) — any number of probes can report to one app, which merges their reports and resolves connections that span hosts. It's not a code gap, it's a deployment gap: only the manager has a probe today.

**Tasks:**
- [ ] Deploy `scope_probe` as a Swarm **global** service (one task per node, same pattern as `portainer_agent`), each pointed at `scope_app` instead of running app+probe combined.
- [ ] Point the probe at `scope_app` over the Swarm overlay network — service-name DNS (e.g. the probe's app-target argument set to `scope_app:4040`), not `127.0.0.1`, since app and probe are now separate containers on separate nodes.
- [ ] Get the image onto every node. Options, in rough order of effort: (a) repeat the `docker save | ssh <host> docker load` dance per node — quick, doesn't scale past this cluster's node count; (b) push to a registry the whole cluster can pull from (need to establish which registry — nothing assumed here, ask before touching any of the user's existing registries/credentials); (c) revisit whether `docker/Dockerfile.scope`'s original Alpine/static/Weave-bundled approach is worth reviving for a smaller, more distributable image — a real `MODERNIZATION-PLAN.md` Phase 6 decision, not one to make casually here.
- [ ] Decide `/var/run/docker.sock` mount policy per node (read-only, matching Stage 1) and confirm each worker's Docker socket is reachable the same way the manager's was.
- [ ] Validate: deploy, then confirm in the UI that a container on `sw-david01` (or `vanguard`) shows a resolved connection to something on the manager or `daya-regia.invis`, not an "Unknown"/pseudo node.

**Definition of Done:** every node in the cluster runs a probe reporting to the central `scope_app`, and the UI shows a real cross-host communication edge between at least two different nodes — the specific thing that prompted this stage.

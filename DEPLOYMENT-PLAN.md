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
| Per-node probe agents (global service) | **Live** — `scope_probe` global service on all 3 worker nodes, `sm-qohelet` covered by `scope_app`'s bundled probe |
| Cross-host communication graph | Mechanism confirmed working (all 4 hosts visible, cross-host adjacency observed for Scope's own probe→app traffic) — an edge between any two *application* containers only appears when real traffic is flowing between them at that moment, which is expected behavior, not a gap |

## Stage 1 — Central app on the manager (done)

**What's deployed:** a single Swarm service, `scope_app`, one replica, constrained to `node.hostname==sm-qohelet`, running app+probe together in one container (`docker/entrypoint-deploy.sh`), `/var/run/docker.sock` mounted read-only, port 4040 published. Live at `http://sm-qohelet.local:4040`.

**How it was built:**
- `docker/Dockerfile.deploy` — a pragmatic multi-stage build, deliberately *not* `docker/Dockerfile.scope` (that one chains through `Dockerfile.cloud-agent` → `weaveworks/cloud-agent`, an Alpine/musl static-linking path bundling Weave Net binaries this deployment doesn't need). Builder is `golang:1.25-bookworm` — `go.mod`'s `go` directive got auto-raised to `1.25.0` by a later `go mod tidy` during `MODERNIZATION-PLAN.md` Phase 2/3 that nobody reconciled with Phase 1's docs (still says 1.22 there); `tools/build/golang/Dockerfile` needs the same bump — tracked as a `MODERNIZATION-PLAN.md` follow-up, not here. Runtime is `debian:bookworm-slim`, not Alpine — the binary is CGO-enabled (`gopacket`/gopcap, `tcptracer-bpf` don't build under `CGO_ENABLED=0`), so it links glibc/libpcap/libnl/libdbus, not musl.
- Client UI baked in from what was already built locally for `MODERNIZATION-PLAN.md` Phase 2's browser validation (`client/build`, `client/build-external`, `prog/staticui`, `prog/externalui`) — not rebuilt inside the Docker build. Rebuilding the client toolchain in-container (Node 10.19-era, needs `NODE_OPTIONS=--openssl-legacy-provider`) is real future work.
- Image transferred via `docker save | ssh sm-qohelet.local docker load` — no registry, so this exact image only exists on that one node. Fine for one replica; **won't scale to Stage 2 without a registry.**
- `.dockerignore` added (excludes `.git`, `client/node_modules`, ~340MB) so the build context doesn't ship irrelevant files.

**Known limitation found live:** with only the manager's probe running, the UI shows only the manager's own containers/processes — no cross-host communication graph to `sw-david01` or `vanguard`, since Scope needs a probe on *both* ends of a connection to resolve it into a real edge instead of leaving one side unresolved. Not a bug — this is exactly what Stage 2 exists to fix.

## Stage 2 — Global per-node probe agent (done)

**Why:** Scope's cross-host correlation is existing, already-built behavior (the probe/app split is the original design, going back to 2015 — see `MODERNIZATION-PLAN.md`'s Phase 0 history) — any number of probes can report to one app, which merges their reports and resolves connections that span hosts. It's not a code gap, it's a deployment gap: only the manager had a probe before this stage.

**What's deployed:**
- `scope_probe`, a Swarm **global** service, one task per node, constrained *away* from `sm-qohelet` (`node.hostname!=sm-qohelet`) since that node already has a probe bundled into `scope_app`. Reuses the same image as Stage 1 (`alfiyansys/scope:modernized-8f6b5774`), same entrypoint binary but with `--entrypoint /usr/bin/scope` overriding `entrypoint-deploy.sh`, running `--mode=probe --probe.docker=true --weave=false --no-app scope_app:4040` — `--no-app` is what suppresses `prog/main.go`'s default `127.0.0.1:<port>` publish target so only the explicit `scope_app:4040` target is used.
- A new overlay network, `scope_net` (`--driver overlay --attachable`), created fresh rather than reusing `portainer_agent_network`/`traefik-public` (not this project's networks to attach to). `scope_app` was updated in place (`docker service update --network-add scope_net scope_app`) to join it — Swarm's embedded DNS only resolves a service name for containers sharing a network with it, which is what makes `scope_app:4040` resolvable from the probes.
- Image loaded onto `sw-david01` and `vanguard` via the same `docker save | ssh <host> docker load` approach as Stage 1 (no registry) — `daya-regia.invis` already had it locally from the build.
- Both services given `--hostname '{{.Node.Hostname}}'` (Swarm's Go-template node-hostname substitution) so the UI shows `sm-qohelet`/`daya-regia`/`sw-david01`/`vanguard` instead of opaque container-ID hostnames.

**Validated:** all 4 nodes now show up as distinct hosts in the UI (`Hosts` topology, 4 nodes) with their real containers correctly attributed per node (confirmed via screenshot: `dvr-window`, `odoo-tune`, `portainer`/`portainer_agent`, `traefik`, `uptime-kuma` all correctly grouped under their actual node). Checked the raw `Endpoint` topology in `/api/report` for genuine cross-host adjacency: the only connections currently observed that cross hosts are Scope's *own* probe→app monitoring traffic (over `scope_net`, ports `:4040`) — which is itself proof the correlation mechanism works, just that there's no other live application-level traffic between, say, `sm-qohelet` and `sw-david01` at any given moment for it to draw. Whether a specific pair of nodes shows an edge depends on whether real traffic is actually flowing between them right then — not something to force, and not a gap in this deployment.

**Not done / deliberately deferred:** image distribution still has no registry (still `save`/`load` per node — fine at 4 nodes, won't scale much further); the `Dockerfile.scope`-vs-`Dockerfile.deploy` base-image decision from Stage 1 is still open.

**Definition of Done:** met. Every node in the cluster runs a probe reporting to the central `scope_app`; the UI shows all 4 real hosts with correctly-attributed containers, and cross-host correlation is confirmed working (via Scope's own inter-probe traffic being visible end-to-end) even though no third-party inter-node app traffic happened to be live at verification time.

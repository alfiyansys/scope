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
| Per-node probe agents (Swarm global service) | **Live** — `scope_probe` on all 3 worker nodes, `sm-qohelet` covered by `scope_app`'s bundled probe |
| Supplementary host-networked probes (Stage 3) | **Live** — `scope-host-probe` (plain `docker run`, not Swarm-managed) on all 4 nodes, for real cross-container traffic visibility Swarm services can't provide |
| Cross-host communication graph | **Confirmed with real traffic** — e.g. `traefik` ↔ `dvr-window` visible as a live cross-node edge, not just Scope's own internal traffic |

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

**Validated:** all 4 nodes show up as distinct hosts in the UI (`Hosts` topology, 4 nodes) with their real containers correctly attributed per node (confirmed via screenshot: `dvr-window`, `odoo-tune`, `portainer`/`portainer_agent`, `traefik`, `uptime-kuma` all correctly grouped under their actual node).

**Known, accepted limitation: no cross-*application*-container traffic graph (e.g. `traefik` → `dvr-window`).** Initially assumed this was just "no live traffic at the moment" — it isn't. Root cause, confirmed by inspecting the raw `Endpoint` topology in `/api/report`: every observed adjacency, even after the capability fix below, is `scope_probe` ↔ `scope_app` traffic on `scope_net` — nothing else. The real reason: Scope's connection tracking (conntrack/eBPF) needs `--net=host` to see the *host's* shared connection-tracking table, where all containers' NAT'd traffic is visible regardless of which Docker network they're on. `docker service create`/`update` has **no `--net=host`, `--pid=host`, or `--privileged` flag at all** — Swarm has never supported any of the three for services (confirmed via `--help` on this Docker 28.3.2). Attached only to its own `scope_net` overlay, the probe is confined to that network's isolated namespace and structurally cannot see traffic on `traefik-public` or `dvr-window`'s network, no matter what capabilities are granted.

Did add what Swarm *does* support — `--cap-add NET_ADMIN NET_RAW SYS_ADMIN SYS_RESOURCE` on both `scope_app` and `scope_probe` — which fixed the constant `conntrack Follow error: operation not permitted` log spam and roughly quintupled tracked endpoints (10 → 48), but all still within `scope_net`. Real, measurable improvement in tracking permission errors, not in cross-service visibility, since that's blocked by network namespace isolation, not permissions.

**Decision (asked, initially confirmed 2026-08-05):** keep this as a proper Swarm service, accepting no deep cross-container traffic graph. **Revisited same session** — the user asked directly whether there was *any* way to still see real traffic (specifically `traefik` → `dvr-window`); see Stage 3 below.

**Not done / deliberately deferred:** image distribution still has no registry (still `save`/`load` per node — fine at 4 nodes, won't scale much further); the `Dockerfile.scope`-vs-`Dockerfile.deploy` base-image decision from Stage 1 is still open.

**Definition of Done:** met. Every node in the cluster runs a probe reporting to the central `scope_app`; the UI shows all 4 real hosts with correctly-attributed containers. Cross-host correlation *between scope's own probes* was confirmed working end-to-end at this stage; real cross-service application traffic came from Stage 3, not this one.

## Stage 3 — Supplementary host-networked probes (done)

**Why:** Stage 2 confirmed the Swarm-service ceiling is real (no `--net=host`/`--pid=host`/`--privileged` for services, ever). Rather than abandon the Swarm-managed deployment to work around it, added a second, *additive* probe per node that isn't Swarm-managed — the Swarm services from Stages 1–2 are untouched.

**What's deployed:** one extra container per node (all 4, including `sm-qohelet` this time — its Swarm-managed probe has the same namespace limitation as the others), named `scope-host-probe`, started via plain `docker run`, not a Swarm service:

```bash
docker run -d --name scope-host-probe --restart=always \
  --net=host --pid=host --privileged \
  --entrypoint /usr/bin/scope \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  alfiyansys/scope:modernized-8f6b5774 \
  --mode=probe --probe.docker=true --weave=false --no-app \
  --probe.log.prefix='<hostprobe>' 127.0.0.1:4040
```

Two things made this work cleanly:
- **`127.0.0.1:4040` as the target, not `scope_app:4040`.** A host-networked container isn't on `scope_net` and can't resolve Swarm service-name DNS. Instead it relies on Swarm's **routing mesh**: `scope_app`'s port was published in default `ingress` mode, so *every* node — including ones that don't run the `scope_app` task — routes local `:4040` traffic to a live instance. Verified this directly (`curl 127.0.0.1:4040/api` returned `HTTP 200` from `sw-david01` and `vanguard`, neither of which run `scope_app`) before relying on it.
- **`--restart=always`, not Swarm-managed.** This is a real gap: unlike the Swarm services, nothing currently guarantees these containers survive a node reboot beyond Docker's own restart policy (which does persist across reboots if the Docker daemon itself is enabled at boot, but there's no `docker service ls` visibility or rolling-update story for these). Acceptable for now; a systemd unit or equivalent per node would close this if it matters later.

**Validated:** immediately eliminated the `conntrack Follow error: operation not permitted` spam (host+pid namespace access is what conntrack actually needs, not just capabilities). Endpoint count went from the Stage 2 ceiling straight to 771 total / 524 non-scope-internal adjacencies — real BitTorrent/VPN/RTSP/Swarm-gossip traffic, not just probe↔app noise. Confirmed the specific thing that prompted this: clicked `traefik_traefik.1` in the UI and it shows a direct, live edge to `dvr-window_dvr-window.1` on `sw-david01` — cross-node, cross-service, real traffic, screenshotted.

**Definition of Done:** met. Real application-level cross-container/cross-host traffic (the `traefik` ↔ `dvr-window` case specifically) is now visible in the UI, without removing or replacing anything from Stages 1–2.

**Follow-up fix (2026-08-06, during `MODERNIZATION-PLAN.md` Phase 4): `scope-host-probe` was still silently missing eBPF connection tracking.** Despite having `--privileged --net=host --pid=host`, the command above never bind-mounted `/sys/kernel/debug`, so `probe/endpoint/ebpf.go`'s kprobe registration failed with `cannot open kprobe_events: no such file or directory` and every node quietly fell back to conntrack/proc scanning (logged as a `WARN`, not actually silent, but easy to miss). Fixed by adding `-v /sys/kernel/debug:/sys/kernel/debug` to the `docker run` command below and recreating the container on all 4 nodes (`daya-regia`, `sm-qohelet`, `sw-david01`, `vanguard`). Confirmed live on all 4: no more eBPF fallback warning in any of their logs.

Current command, live on all 4 nodes as of 2026-08-06:
```bash
docker run -d --name scope-host-probe --restart=always \
  --net=host --pid=host --privileged \
  --entrypoint /usr/bin/scope \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v /sys/kernel/debug:/sys/kernel/debug \
  alfiyansys/scope:modernized-8f6b5774 \
  --mode=probe --probe.docker=true --weave=false --no-app \
  --probe.log.prefix='<hostprobe>' 127.0.0.1:4040
```

One thing to know if this container ever needs a manual restart: kprobes registered via `/sys/kernel/debug/tracing/kprobe_events` are host-global, not container-scoped. A container killed hard enough to skip its own cleanup can leave stale kprobes behind, which makes the *next* attempt fail with `cannot write ...: file exists` — clear them with `echo > /sys/kernel/debug/tracing/kprobe_events` (needs a privileged container or root) before retrying if that happens.

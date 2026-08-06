# Scope Fork — Live Deployment Plan (sm-qohelet Swarm)

Separate from `MODERNIZATION-PLAN.md`. That file tracks bringing the
*codebase* up to date (Go toolchain, Docker client, Swarm/K8s/eBPF
support). This file tracks actually **running** this fork as real,
standing infrastructure on the user's Swarm cluster (manager
`sm-qohelet.local`, workers `daya-regia.invis`, `sw-david01`,
`vanguard`) — a different, ongoing concern: deployment topology,
image distribution, and operational follow-ups, not code changes.

Also covers non-Swarm hosts that report into the same central app
over Tailscale (see Stage 5) — not cluster members, just additional
probes.

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

Command as of this fix (image source superseded a few hours later by Stage 4 below — see there for the current `ghcr.io`-based command):
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

## Stage 4 — GHCR image distribution (done)

**Why:** Stages 1–3 all distributed the image via `docker save | ssh <host> docker load` — no registry, so every rebuild meant manually copying a ~145MB tarball to each of the 4 nodes by hand. Flagged as a known limitation since Stage 2 ("won't scale much further"). Replaced with GitHub Container Registry.

**What changed:** pushed the existing image to `ghcr.io/alfiyansys/scope:modernized-8f6b5774` (and `:latest`), then set the package to **public** on GitHub so no node needs its own registry credentials to pull — the image only contains the compiled `scope` binary and UI assets, nothing sensitive. Verified public, unauthenticated pull worked from a node with zero `ghcr.io` login (`sm-qohelet.local`) before rolling anything out further.

Updated all 4 nodes to the registry image, same digest as what was already running (`sha256:ea8792c3...`), so this was a distribution-path change, not a code/behavior change:
- `docker service update --image ghcr.io/alfiyansys/scope:modernized-8f6b5774 scope_app` and `scope_probe` — both converged clean, no errors.
- Recreated `scope-host-probe` on all 4 nodes (`sm-qohelet`, `daya-regia`, `sw-david01`, `vanguard`) with the same `docker run` command as Stage 3, just pointing at `ghcr.io/alfiyansys/scope:modernized-8f6b5774` instead of the locally-tagged `alfiyansys/scope:modernized-8f6b5774`. eBPF still loads cleanly on all 4 (no fallback warning), confirming the debugfs-mount fix from Phase 4 survived the swap.

**Validated:** `curl http://127.0.0.1:4040/api` returns `HTTP 200` on all 4 nodes post-cutover. One transient blip during validation — `scope_app`'s routing-mesh path timed out for about 15 seconds right after the burst of service updates and container recreations across the cluster, while the app itself was already serving fine internally (`docker exec ... curl` returned `200` the whole time) — settled on its own, not a real regression.

**What this unblocks:** future rebuilds are now `docker build && docker push`, then a `docker service update --image ...` / container recreate per node — no more manual `save`/`load`. The `Dockerfile.scope`-vs-`Dockerfile.deploy` base-image decision (still open, noted in Stage 1/2) and the missing systemd unit for `scope-host-probe`'s reboot survival (Stage 3) remain the two open follow-ups.

**Definition of Done:** met. All 4 nodes pull `ghcr.io/alfiyansys/scope:modernized-8f6b5774` directly; `docker save`/`load` is no longer part of the deployment path.

## Stage 5 — `aqila-linvis` probe over Tailscale (done, 2026-08-06)

**Why:** first host outside the sm-qohelet Swarm cluster to run a probe. `aqila-linvis.hs.ian` is on a different LAN (no mDNS reachability to `sm-qohelet.local`), not a Swarm member, and joining it to the Swarm wasn't the goal — it just needed to report into the same central `scope_app` for visibility. Both hosts already had Tailscale, giving a ready-made path without touching Swarm membership or opening anything to the public internet.

**What's deployed:** same Stage 3 pattern (`docker run`, not Swarm-managed) — `--net=host --pid=host --privileged`, Docker socket and `/sys/kernel/debug` mounted (this host already had debugfs mounted, so no Stage-3-style eBPF-fallback fix needed), same `ghcr.io/alfiyansys/scope:modernized-8f6b5774` image:

```bash
docker run -d --name scope-host-probe --restart=always \
  --net=host --pid=host --privileged \
  --entrypoint /usr/bin/scope \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v /sys/kernel/debug:/sys/kernel/debug \
  ghcr.io/alfiyansys/scope:modernized-8f6b5774 \
  --mode=probe --probe.docker=true --weave=false --no-app \
  --probe.log.prefix='<hostprobe>' qohelet.home:4040
```

**Target resolution:** `qohelet.home:4040`, not `sm-qohelet.local` (unreachable — different LAN) and not the `127.0.0.1:4040`-via-routing-mesh trick Stage 3 used (`aqila-linvis` isn't a Swarm node, so there's no local mesh endpoint). `sm-qohelet` has its own direct Tailscale identity, `qohelet.home` — confirmed with a `curl .../api` 200 from `aqila-linvis` before deploying. Considered routing through `daya-regia`'s Tailscale address (also reachable) and letting the Swarm routing mesh forward it, same as Stage 3's local trick, but going straight to the manager's own Tailscale identity is more direct — one less hop, no dependency on `daya-regia` staying in the cluster.

**Validated:** probe logs show a clean `Control connection to qohelet.home starting` / `Publish loop for qohelet.home starting`, no eBPF-fallback warning (kprobes registered fine, debugfs was already mounted). Confirmed registered via `curl http://qohelet.home:4040/api/topology/hosts` from `aqila-linvis` itself — `aqila-linvis` appears alongside all 4 Swarm-cluster hosts as its own node.

**Known limitation, same root cause as Stage 2:** no cross-host traffic graph between `aqila-linvis` and the Swarm cluster hosts — they're on different LANs with only a Tailscale probe↔app control/publish channel between them, not a shared L2/L3 network Scope's conntrack/eBPF tracking could correlate across. This host's own local container traffic is visible; cross-cluster correlation is not, and isn't expected to be (no shared network path exists for it to observe).

**Not done:** no systemd unit (same accepted gap as Stage 3 — `--restart=always` covers daemon restarts, not host reboots unless Docker itself is enabled at boot, which it is on other nodes by distro default but wasn't specifically verified here).

**Definition of Done:** met. `aqila-linvis` runs a probe reporting into the existing central `scope_app`, visible in the UI as a 5th host, without any change to Swarm membership or the other 4 nodes.

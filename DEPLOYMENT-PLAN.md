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

## Resource usage baseline & optimization plan (2026-08-06)

Measured live via `docker stats --no-stream` across every node currently running a scope process — the 4 Swarm members plus `aqila-linvis` (Stage 5). Not yet acted on; this is the plan, execution is future work.

**Baseline snapshot:**

| Node | vCPUs | Container | CPU % | Mem | Mem % | PIDs |
|---|---|---|---|---|---|---|
| `sm-qohelet` (manager) | 2 | `scope_app` (app + bundled probe) | 4.83% | 262.5 MiB | 13.34% | 22 |
| `sm-qohelet` | 2 | `scope-host-probe` | 2.88% | 88.2 MiB | 4.48% | 14 |
| `daya-regia` | 12 | `scope_probe` (Swarm global) | 0.53% | 54.2 MiB | 0.17% | 21 |
| `daya-regia` | 12 | `scope-host-probe` | 13.83% | 138.9 MiB | 0.43% | 22 |
| `sw-david01` | 4 | `scope_probe` (Swarm global) | 0.92% | 52.2 MiB | 1.33% | 15 |
| `sw-david01` | 4 | `scope-host-probe` | 2.68% | 57.1 MiB | 1.46% | 15 |
| `vanguard` | 4 | `scope_probe` (Swarm global) | 0.28% | 51.1 MiB | 0.32% | 14 |
| `vanguard` | 4 | `scope-host-probe` | 2.48% | 56.4 MiB | 0.35% | 15 |
| `aqila-linvis` (Tailscale) | 4 | `scope-host-probe` | 6.19% | 58.1 MiB | 0.74% | 15 |

Fleet total: ~819 MiB resident across 9 processes on 5 hosts. No single node is under real memory pressure from this today — the finding here is about avoidable duplication and unbounded worst-case, not a current outage risk.

**1. Every Swarm node runs two full probes doing overlapping work — the biggest lever.** `scope-host-probe` (Stage 3) was added *specifically because* Swarm-managed probes structurally can't get `--net=host`/`--pid=host` and therefore can't see real cross-container traffic (Stage 2's own finding). Since then, every node has kept running *both*: the original Swarm-managed probe (`scope_probe` global service on the 3 workers; `scope_app`'s bundled probe, launched via `docker/entrypoint-deploy.sh`'s backgrounded `scope --mode=probe &`, on the manager) *and* the strictly-more-capable host-probe. The host-probe's container/process topology comes from the same `--probe.docker=true` + `/proc` scanning either way — the Swarm-managed probes aren't seeing anything unique, just a subset.
   - Dropping the `scope_probe` global service entirely would recover ~157 MiB (54.2 + 52.2 + 51.1) plus its CPU share across `daya-regia`/`sw-david01`/`vanguard`, for zero loss of visibility.
   - `scope_app`'s bundled probe (confirmed `--no-probe` exists as a flag) could be dropped from `entrypoint-deploy.sh` the same way, redundant with `sm-qohelet`'s own separate `scope-host-probe` — the highest-value cut since `sm-qohelet` is the most CPU-constrained node in the fleet (2 vCPUs) *and* shares it with real workloads (`traefik`, `portainer`, `dvr-window`, `mediamtx`).
   - **Two latent (non-obvious) factors found while digging into *why* this redundancy costs more than "two containers exist," verified live, not inferred:**
     - **Duplicate raw packet capture.** `probe/endpoint/dns_snooper.go:40-55` unconditionally opens its own `pcap` handle (`"any"` interface, `inbound and port 53` BPF filter) and an 8 MB kernel ring buffer (`bufSize`, line 23) every time a probe process starts (`prog/probe.go:262`, gated only by not being in `kubernetesRole == cluster` mode — every probe hits this). Confirmed live on `daya-regia`: `docker exec scope-host-probe cat /proc/net/packet` **and** the same check against the `scope_probe` container both show an active `AF_PACKET` socket (`Proto 0003`) *at the same time* — both processes are independently capturing and parsing every DNS packet on that host, right now. Not shared, not deduped — literally double the packet-parsing CPU and kernel buffer memory for identical DNS records, on every node running both probes.
     - **The Swarm-managed probes never got Phase 4's eBPF fix.** Rechecked current logs: `scope_app` and `scope_probe` are *still* logging `Error setting up the eBPF tracker, falling back to proc scanning` (`mmap error: operation not permitted` on `daya-regia`'s `scope_probe`; `cannot open kprobe_events: no such file or directory` on `scope_app`) — the `/sys/kernel/debug` bind mount from Phase 4 was only ever added to the standalone `scope-host-probe` containers (Stage 3/4's `docker run` commands), never to the two Swarm services (`docker service inspect scope_app|scope_probe --format '{{json .Spec.TaskTemplate.ContainerSpec.Mounts}}'` shows only the Docker-socket mount, no debugfs). So the redundant probes aren't just redundant in *what* they see — they're doing it with conntrack/proc scanning, the strictly more expensive collection mechanism, instead of eBPF. (Swarm services *can* do bind mounts, unlike `--privileged`/`--net=host`/`--pid=host` — this would be a legitimate incremental fix if #1 isn't fully executed, but the real fix is still just removing them.)
   - **Checked and ruled out, not a leak:** the DNS reverse-lookup cache behind `CachedNamesForIP` (`probe/endpoint/dns_snooper.go:45`) is a proper bounded LRU (`gcache`, cap `maxReverseDNSrecords = 10000`), and its decoding-error counter is separately capped (`maxDecodingErrorCardinality = 1000`, line 26). No unbounded growth here despite the `// TODO: Be smarter about the expiration of entries with pre-existing associated domains` at line 289 — that TODO is about eviction *smartness*, not a missing bound.
   - **Before cutting anything:** validate that `tagger.go`'s Swarm-service-node labeling (container-label-based, confirmed in Phase 3) still populates correctly from the host-probe alone — expected to, since it reads the same container labels regardless of which probe container does the reading, but should be confirmed against a real `/api/topology/swarm-services` check before removing the Swarm-managed probes, not assumed.

**2. No CPU or memory limits are configured anywhere.** Swarm services show `Limits: {}`/`Reservations: {}`; `scope-host-probe`'s plain `docker run` sets no `--memory`/`--cpus` either. Not an active problem today, but `scope_app` is already the single heaviest container measured (4.83% CPU / 13.34% of node memory) on the one node with the least headroom to give, with no ceiling if report volume spikes. Setting explicit limits (Swarm `Resources.Limits`, `docker run --memory --cpus`) bounds the worst case and is close to free to add.

**3. `GOMAXPROCS` is unconstrained, most visible on `daya-regia` (12 vCPUs).** A single-purpose collector defaults to matching the whole host's core count, more GC/scheduler overhead than this workload needs. Fixing #2 (real CPU limits) is the clean way to address this too — the Go runtime this fork builds with is cgroup-quota-aware, so it right-sizes `GOMAXPROCS` automatically once a real quota exists, rather than needing a hardcoded value.

**4. Tunable report/spy intervals exist and are still at their defaults.** `--probe.publish.interval` (default `3s`) and `--probe.spy.interval` (default `1s`) trade topology freshness for CPU/network directly, with no code change needed — a safe, reversible lever if any node needs throttling later. Not recommending changing these now (nothing currently justifies it); noting them since they're the easiest knob available if #1 alone doesn't create enough headroom.

**5. `scope-host-probe`'s CPU cost tracks real traffic volume, not waste.** `daya-regia`'s 13.83% vs. the other nodes' ~2.5–6% lines up with it being the node seeing the most real cross-container/BitTorrent/VPN/RTSP traffic (Stage 3's own 771-endpoint count was measured there). This is the tool doing its job, not a leak — the interval tuning in #4 is the lever if this specific node ever needs to be throttled back.

**6. Image size (145 MB)** is a secondary, disk/pull-time concern rather than a runtime CPU/memory one — cross-references `MODERNIZATION-PLAN.md` Phase 6's still-open `Dockerfile.scope`-vs-`Dockerfile.deploy` base-image decision rather than duplicating it here.

**Priority order for execution (not yet done):** #1 (drop redundant Swarm-managed probes, after the `tagger.go` validation check) is the only change with a real, measurable payoff; #2 is cheap insurance worth doing alongside it; #3 falls out of #2 for free; #4 and #5 are informational, act on them only if #1 doesn't create enough headroom on `sm-qohelet` specifically.

### Code-level findings (not just deployment config)

Everything above is deployment/config-only. Actually read the Go code (`app/`, `probe/`, `report/`) for code-level resource costs, not just guessed — each finding below is cited against real lines, verified by reading the file, not inferred:

- **Real finding: the app does a full deep-copy of the merged report on every websocket tick, unconditionally.** `app/api_topology.go:139` ticks every `websocketLoop = 1*time.Second` (line 22) per open browser tab; each tick calls `wc.update()` → `wc.rep.Report()` → `app/collector.go:146` (cache hit) or `:157` (fresh merge) — both paths return `c.cached.Copy()`, a full deep copy of every `Topology`'s node map (`report/topology.go`'s `Copy()`, which allocates a fresh `map[string]Node` per topology). There's no dirty-check before this — it re-copies and re-renders even if nothing changed since the last tick. Cost scales with (open UI tabs) × (report size) × 1/sec. For this deployment's actual usage (a personal dashboard, realistically 0-1 tabs open most of the time) this is a real inefficiency but a low-impact one; would matter more if multiple people kept the UI open simultaneously.
- **Minor hygiene nit, not a real leak:** `probe/endpoint/resolver.go:36` uses the classic `time.Tick(time.Second/10)` anti-pattern (no way to `Stop()` it) for reverse-DNS throttling. Harmless in practice since it's a process-lifetime singleton, not something that accumulates, but `time.NewTicker` + deferred `Stop()` would be the idiomatic fix if this file is ever touched for another reason.
- **Merge/codec paths checked and are fine:** `app/merger.go`'s `fastMerger.Merge` mutates in place (`UnsafeMerge`) rather than copying, and `report/topology.go`'s `Nodes.Merge` is O(n) per topology, not quadratic — no algorithmic issue found. The hand-written codec methods added across Phase 1 (`report/node_set.go`, `report/sets.go`, `report/backcompat.go`) all delegate to the generated codecgen path deliberately; no reflection-fallback smell.
- **pprof is already wired up, just not being used.** `prog/app.go:64` mounts `/debug/pprof` on the app's router (uses whatever `--app.basic-auth` is already configured, no separate lockdown). The probe has the same (`prog/probe.go:88-95`) but only if started with `--probe.http.listen=:<port>` (off by default). If any of the above is ever worth confirming with real numbers instead of reading code, `go tool pprof http://sm-qohelet.local:4040/debug/pprof/heap` against the live `scope_app` is already available with zero code changes.

**None of these code-level findings are worth acting on before the deployment-level #1 (redundant probes)** — the websocket copy-per-tick only matters if several people keep the UI open at once, which isn't this deployment's actual usage pattern. Noted here so it's not silently missed, not because it's the priority.

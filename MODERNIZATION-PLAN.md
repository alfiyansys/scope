# Scope Fork — Modernization Plan

Goal: bring this fork of `weaveworks/scope` back to working order against
**current Docker Engine / Moby releases (26.x+), current Docker Swarm mode,
and current Kubernetes**. Upstream is deprecated (last real commit
2023-06-13, `12175b96`) and the dependency tree is frozen at roughly
2018–2020 vintage.

Phases are ordered by dependency — do them in order. Each phase has:
**Tasks** (concrete, file-scoped), **Commands** (what to actually run),
and **Definition of Done** (how you know the phase is finished, not just attempted).

## Baseline (confirmed by inspecting this repo)

| Component | Current pin | Problem |
|---|---|---|
| Go | `go 1.16` (go.mod) | 4 language versions behind; toolchain image is `golang:1.14.4-stretch` (EOL Debian) |
| Docker client | `fsouza/go-dockerclient v1.3.0` + `docker/docker@0c5f8d2b` (Aug 2018) | ~5+ Engine API versions behind, used in `probe/docker/{registry,container,reporter,controls,tagger}.go`, `app/weave.go` |
| Kubernetes client | `k8s.io/client-go v10`, `k8s.io/kubernetes v1.13.0` | pre-1.14 era; also wrongly depends on the whole `k8s.io/kubernetes` server repo, not just client libs |
| eBPF tracer | `weaveworks/tcptracer-bpf` + `iovisor/gobpf` (`probe/endpoint/ebpf.go`) | legacy kprobe BPF, not CO-RE — needs matching kernel headers, likely dead on 5.x/6.x kernels |
| Swarm support | `render/swarm.go`, `probe/docker/{reporter,tagger}.go`, `report/report.go` | code exists, not broken — just unvalidated against modern Swarm/Engine API |
| CI | none | CircleCI config was deleted in the deprecation commit |
| Base image | `weaveworks/cloud-agent` (`docker/Dockerfile.scope`) | unmaintained upstream base |

---

## Phase 0 — CI safety net

**Why:** every later phase needs a pass/fail signal, or you're upgrading blind.

**Tasks:**
- [ ] Add `.github/workflows/ci.yml`: `go build ./...`, `go vet ./...`, `go test ./...` on push/PR.
- [ ] Run `make` locally once to record which targets currently work at all, before touching anything.
- [ ] Decide on `vendor/`: either keep it synced (`go mod vendor` after every dep change) or delete it and let CI use the module proxy/cache. Recommend deleting — it's ~4x repo size and will drift silently otherwise.

**Commands:**
```bash
go build ./... 2>&1 | tee /tmp/baseline-build.log
go vet ./... 2>&1 | tee /tmp/baseline-vet.log
```

**Definition of Done:** a CI workflow runs green (or with a known/documented failure list) on the *current*, unmodified code — this is your regression baseline for every phase below.

---

## Phase 1 — Go toolchain

**Why:** everything downstream (module resolution, security patches, generics-era libraries) needs a current Go.

**Tasks:**
- [ ] `go.mod`: bump `go 1.16` → `go 1.22` (or 1.23).
- [ ] `tools/build/golang/Dockerfile`: replace `FROM golang:1.14.4-stretch` with `FROM golang:1.22-bookworm`.
- [ ] Replace the `go get github.com/...` tool-install block in that Dockerfile with `go install pkg@version` — `go get` for tools was removed as a pattern in Go 1.17+.
- [ ] Grep for and remove any remaining `github.com/golang/dep` (pre-modules tool) invocations in `Makefile`/`tools/`.
- [ ] Fix compiler/vet breakage from the version jump (expect `x/sys`, `x/net`, `io/ioutil` deprecation warnings).

**Commands:**
```bash
grep -rn "golang/dep\|\bdep ensure\b" Makefile tools/ --include="*.sh" --include="Makefile*"
sed -i 's/^go 1.16$/go 1.22/' go.mod
go build ./... 2>&1 | tee /tmp/phase1-build.log
```

**Definition of Done:** `go build ./...` and `go vet ./...` succeed with Go 1.22 with no new errors vs. the Phase 0 baseline; CI image builds and runs.

---

## Phase 2 — Docker client & Engine API compatibility

**Why:** this is the actual "newest Docker runtime" requirement. Everything else is plumbing to get here.

**Tasks:**
- [ ] Replace `fsouza/go-dockerclient` with the official `github.com/docker/docker/client` SDK (preferred — actively tracks Engine API releases) in:
  - `probe/docker/registry.go` (client construction — currently `NewClientFromEnv`/`NewClient`)
  - `probe/docker/container.go` (container inspect/stats)
  - `probe/docker/controls.go` (start/stop/pause/restart/exec controls)
  - `probe/docker/reporter.go`, `probe/docker/tagger.go`
  - `app/weave.go`
- [ ] Set an explicit API version floor via `client.WithVersion("1.43")` (or negotiate with `client.WithAPIVersionNegotiation()`) instead of relying on whatever the old library defaulted to.
- [ ] Diff struct fields used from container/network inspect against the current `docker/docker/api/types` — field renames/removals between 2018 and now are the likely breakage source.
- [ ] Manually validate against a real current Engine: container list/inspect, stats stream, exec-into-container, pause/stop/restart controls.

**Commands (validation, once code changes land):**
```bash
docker version --format '{{.Server.APIVersion}}'   # confirm target Engine's API version
DOCKER_API_VERSION=1.43 ./scope launch
# then hit http://localhost:4040 and check containers render, stats update, exec works
```

**Definition of Done:** scope's app+probe run against a current `dockerd` (containerd-backed, cgroup v2 host) with container topology, live stats, and container controls (pause/stop/restart/exec) all functioning — verified manually against a real Engine, not just compiling.

---

## Phase 3 — Docker Swarm mode

**Why:** Swarm plumbing already exists (it is *not* missing) — this phase validates/repairs it now that Phase 2 modernized the client underneath it.

**Tasks:**
- [ ] Stand up a local Swarm (`docker swarm init`), deploy a small multi-service stack.
- [ ] Verify `probe/docker/reporter.go`/`tagger.go` still correctly populate `report.SwarmService` nodes against current `ServiceList`/`TaskList` API responses (field additions since 2018 are additive/safe, but confirm nothing was renamed).
- [ ] Verify `render/swarm.go`'s `SwarmServiceRenderer` produces correct topology across current task states (Swarm has added states like `remove`, rolling-update fields).
- [ ] Check `report/report.go` / `report/id.go` for whether a Swarm **node** (manager/worker) topology exists; if not, scope it as a follow-up rather than blocking this phase on it.
- [ ] Add a repeatable integration check under `integration/` that stands up Swarm and asserts the probe reports services — this path has had zero live testing in years and needs a regression guard going forward.

**Commands:**
```bash
docker swarm init
docker service create --name web --replicas 3 nginx:alpine
# then confirm scope's UI/API shows the swarm service topology
curl -s http://localhost:4040/api/topology/swarm-services | jq .
```

**Definition of Done:** a live 3-node (or single-node dev) Swarm cluster with at least 2 services shows correct service topology in scope's UI, and an automated integration check codifies this so it doesn't silently rot again.

---

## Phase 4 — eBPF endpoint tracking (highest technical risk)

**Why:** `probe/endpoint/ebpf.go` uses legacy kprobe-based BPF that compiles against a specific kernel's headers — this is the piece most likely to be silently dead weight on any modern kernel.

**Tasks:**
- [ ] On a current kernel (5.x/6.x), attempt to load the existing eBPF tracer and confirm whether it works, fails loudly, or fails silently (check logs for BPF verifier/load errors).
- [ ] Check whether `probe/endpoint/` already has a non-eBPF fallback (proc-based connection scanning); if the eBPF path is failing silently today, that's a correctness bug independent of modernization.
- [ ] If repairing: port to `cilium/ebpf` (CO-RE, compile-once-run-everywhere) instead of `iovisor/gobpf`.
- [ ] If not repairing now: make the eBPF path an explicit opt-in with a loud startup log on failure, so it degrades safely instead of silently under-reporting connections.
- [ ] Benchmark whichever path ends up default (eBPF exists for a performance reason at high connection counts — confirm the fallback's cost before deciding to drop eBPF).

**Commands:**
```bash
uname -r
dmesg | grep -i bpf | tail -20
# run scope with eBPF enabled and check probe logs for load/verifier errors
```

**Definition of Done:** either eBPF tracing demonstrably works on a current kernel with a documented minimum kernel version, or it's explicitly disabled with a clear fallback and a tracked follow-up — not silently broken.

---

## Phase 5 — Kubernetes client modernization (can run parallel to Phase 4)

**Why:** `k8s.io/client-go v10`/`k8s.io/kubernetes v1.13.0` predates several stable API removals; also `k8s.io/kubernetes` as a dependency is a known anti-pattern that drags in the whole server codebase.

**Tasks:**
- [ ] Upgrade `k8s.io/client-go`, `k8s.io/api`, `k8s.io/apimachinery` to a current matched release set (e.g. `v0.30.x`).
- [ ] Audit `probe/kubernetes/` for what `k8s.io/kubernetes` is actually imported for; replace those specific usages with the equivalent `k8s.io/api/...` types and drop the `k8s.io/kubernetes` dependency entirely.
- [ ] Grep for `extensions/v1beta1` (old Deployments/Ingress/DaemonSets/ReplicaSets path) and migrate to `apps/v1` / `networking.k8s.io/v1` — the old group was removed from Kubernetes well before current versions and will hard-fail against a modern API server.
- [ ] Re-test `examples/k8s/` manifests (RBAC roles, service account bindings) against a current cluster — API group names in `ClusterRole` rules likely need updating alongside the code.

**Commands:**
```bash
grep -rn "extensions/v1beta1" probe/kubernetes/ examples/k8s/
go get k8s.io/client-go@v0.30.0 k8s.io/api@v0.30.0 k8s.io/apimachinery@v0.30.0
go mod tidy
```

**Definition of Done:** scope's Kubernetes probe connects to a current-minor kubeadm/kind/EKS cluster, and pod/deployment/replicaset/service topologies render correctly with no API-group errors in probe logs.

---

## Phase 6 — Base images, packaging, dependency cleanup

**Why:** cosmetic/hygiene relative to Phases 1–5, but blocks a trustworthy release artifact.

**Tasks:**
- [ ] Replace `FROM weaveworks/cloud-agent` in `docker/Dockerfile.scope` with a maintained base (plain `alpine`, explicit `runit` package install, or drop `runit` and run app+probe as separate processes/containers).
- [ ] Decide fate of the Weave Net overlay integration (`app/weave.go`, vendored `weaveworks/weave` v2.3.1): Weave Net is itself unmaintained, so either update the vendored version or make the integration clearly optional/off-by-default and stop building it in by default.
- [ ] Run `govulncheck ./...` and address flagged deps — known-stale candidates already visible in `go.mod`: `hashicorp/consul` (pre-1.0 pin), `nats-io/nats` (pre-1.0 client), `aws/aws-sdk-go` v1 (consider v2 if the ECS probe code is kept).

**Commands:**
```bash
go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...
docker build -f docker/Dockerfile.scope -t scope:modernized .
```

**Definition of Done:** `docker/Dockerfile.scope` builds from a currently-maintained base image, `govulncheck` shows no unaddressed high/critical findings, and the Weave Net integration's status (kept-and-updated vs. optional-and-deprioritized) is an explicit decision, not an accident.

---

## Execution order

```
Phase 0 (CI baseline)
   └─▶ Phase 1 (Go toolchain)
          └─▶ Phase 2 (Docker client)  ─────────┐
                 └─▶ Phase 3 (Swarm validation)  │
                                                  ├─▶ Phase 6 (cleanup)
          └─▶ Phase 4 (eBPF, isolated) ──────────┤
          └─▶ Phase 5 (Kubernetes)  ─────────────┘
```

Phases 2 and 3 together are what actually deliver "works on the newest Docker
runtime, including Swarm." Phase 4 (eBPF) and Phase 5 (Kubernetes) don't
depend on each other or on Phase 3, so they can run in parallel once Phase 1
is done. Treat Phase 6 as final polish, not a blocker for a working build.

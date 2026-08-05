#!/bin/bash
# Repeatable manual check for Phase 3 of ../MODERNIZATION-PLAN.md.
#
# NOT part of the automated GCE/Vagrant-provisioned suite in this directory
# (deliberately doesn't match *_test.sh, so tools/integration/run_all.sh
# won't try to pick it up - that harness needs infrastructure this script
# doesn't, and per CONTRIBUTING.md is already hard to set up even for
# maintainers). This instead validates against an EXISTING Docker Swarm
# cluster reachable over SSH, and a scope app+probe already running
# against the local Docker daemon of a node in that cluster.
#
# What it checks: deploys a small, disposable Swarm service, confirms it
# shows up in scope's /api/topology/swarm-services within a timeout, then
# removes the service again. Exists because this exact path (SwarmService
# topology rendering) had a real bug that stayed invisible for years:
# probe/docker/tagger.go added nodes without report.Node.Topology set, so
# report.UnsafeRemovePartMergedNodes silently dropped every single one -
# see MODERNIZATION-PLAN.md Phase 3 for the full writeup.
#
# Usage:
#   SWARM_MANAGER_HOST=sm-qohelet.local SCOPE_URL=http://localhost:4040 \
#     ./integration/swarm-live-validation.sh
#
# Requires: SSH access (batch/key-based) to a Swarm manager, and a scope
# app+probe already running against a Docker daemon that's a member of
# that same swarm (doesn't need to be the manager - worker nodes work,
# since the SwarmService topology is built purely from container labels).

set -euo pipefail

SWARM_MANAGER_HOST="${SWARM_MANAGER_HOST:?set SWARM_MANAGER_HOST to a Swarm manager reachable over SSH}"
SCOPE_URL="${SCOPE_URL:-http://localhost:4040}"
SERVICE_NAME="${SERVICE_NAME:-scope-swarm-validation}"
LOCAL_HOSTNAME="${LOCAL_HOSTNAME:-$(hostname)}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-60}"

cleanup() {
	echo "Removing test service $SERVICE_NAME..."
	ssh -o BatchMode=yes "$SWARM_MANAGER_HOST" "docker service rm $SERVICE_NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "Deploying disposable test service $SERVICE_NAME (constrained to $LOCAL_HOSTNAME)..."
ssh -o BatchMode=yes "$SWARM_MANAGER_HOST" \
	"docker service create --name $SERVICE_NAME --replicas 1 --constraint 'node.hostname==$LOCAL_HOSTNAME' nginx:alpine" \
	>/dev/null

echo "Waiting up to ${TIMEOUT_SECONDS}s for $SERVICE_NAME to appear in $SCOPE_URL/api/topology/swarm-services..."
deadline=$((SECONDS + TIMEOUT_SECONDS))
found=false
while [ "$SECONDS" -lt "$deadline" ]; do
	if curl -s "$SCOPE_URL/api/topology/swarm-services" | grep -q "\"label\":\"$SERVICE_NAME\""; then
		found=true
		break
	fi
	sleep 3
done

if [ "$found" = true ]; then
	echo "PASS: $SERVICE_NAME appeared in the Swarm services topology."
	exit 0
else
	echo "FAIL: $SERVICE_NAME never appeared within ${TIMEOUT_SECONDS}s. Current response:"
	curl -s "$SCOPE_URL/api/topology/swarm-services"
	exit 1
fi

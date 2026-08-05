#!/bin/sh
# Runs app+probe together in one container, matching the manager-node
# deployment step in MODERNIZATION-PLAN.md Phase 3's follow-up. The probe
# talks to the app over 127.0.0.1 since they share this container's network
# namespace - simplest thing that works for a single-node deployment.
#
# Not a real process supervisor: if the app dies, this container dies with
# it (probe is backgrounded, `exec` replaces this shell with the app so
# Docker's own restart policy handles the app process; the probe is not
# independently supervised). Fine for now; revisit if/when this becomes the
# global per-node agent deployment.
set -e
/usr/bin/scope --mode=probe --probe.docker=true --weave=false &
exec /usr/bin/scope --mode=app

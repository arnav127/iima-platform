#!/usr/bin/env bash
# Saves Nomad's cluster state (jobs, variables, ACLs) and keeps the newest 14.
# Run daily by nomad-snapshot.timer on each server; only the leader's copy matters, so the
# others' are a spare. Copy /srv/iima/backups off the cluster with the other backups.
set -euo pipefail
dir=/srv/iima/backups
mkdir -p "$dir"
NOMAD_TOKEN="$(cat /etc/nomad.d/snapshot.token)" \
  /usr/local/bin/nomad operator snapshot save "$dir/nomad-$(date +%Y%m%d-%H%M).snap"
ls -1t "$dir"/nomad-*.snap | tail -n +15 | xargs -r rm --

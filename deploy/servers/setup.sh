#!/usr/bin/env bash
# Sets up one campus server for the platform. Run as root on each machine, from a checkout of
# iima-platform (servers are Linux; this is the one bash script in the repo).
#
#   sudo NODE_NAME=iima-node-1 NODE_IP=10.0.0.11 NET_IFACE=eth0 \
#        SERVER_IPS=10.0.0.11,10.0.0.12,10.0.0.13 CLUSTER_CIDR=10.0.0.0/24 \
#        ROLE=server ENTRY=true ./deploy/servers/setup.sh
#
# ROLE=server on three machines (Nomad server + client), ROLE=client on the fourth.
# ENTRY=true on exactly one machine: it also runs Traefik on ports 80 and 443.
# FIREWALL=apply loads the nftables rules; without it they are only written to
# /etc/nftables.d/iima.nft for you to read first (a wrong rule can lock you out of SSH).
#
# Safe to run again: it rewrites config and restarts services, never touches data.
set -euo pipefail

NOMAD_VERSION=2.0.7
NOMAD_SHA256_LINUX_AMD64=4c9b8a0850d6fd9caadbbab09b3e6fdf8b77aa777729543c70c61b85acca68c1
TRAEFIK_VERSION=v3.7.14
TRAEFIK_SHA256_LINUX_AMD64=d08641f5de2dd3788097bddab1ce4b29c10a0b788566970db707fa5c94c71f15
NODE_MAJOR=22

: "${NODE_NAME:?}" "${NODE_IP:?}" "${NET_IFACE:?}" "${SERVER_IPS:?}" "${CLUSTER_CIDR:?}"
ROLE="${ROLE:-client}"
ENTRY="${ENTRY:-false}"
REPO="$(cd "$(dirname "$0")/../.." && pwd)"
say() { printf '\n==> %s\n' "$*"; }
die() { printf '\nsetup: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "run as root"
[ "$(uname -m)" = x86_64 ] || die "only x86_64 is pinned below"
case "$ROLE" in server|client) ;; *) die "ROLE must be server or client" ;; esac

if command -v dnf >/dev/null; then PKG="dnf -y install"; GETTEXT=gettext
elif command -v apt-get >/dev/null; then PKG="apt-get -y install"; GETTEXT=gettext-base; apt-get update -q
else die "need dnf or apt-get"; fi

say "packages"
$PKG curl unzip tar nftables python3 "$GETTEXT" >/dev/null

# Pinned download with checksum, into /usr/local/bin.
install_bin() { # name url sha256 inner-path
  local tmp; tmp="$(mktemp -d)"
  curl -fsSL -o "$tmp/archive" "$2"
  echo "$3  $tmp/archive" | sha256sum -c - >/dev/null || die "checksum mismatch for $1"
  case "$2" in
    *.zip) unzip -q -o "$tmp/archive" -d "$tmp/x" ;;
    *) mkdir -p "$tmp/x" && tar -xzf "$tmp/archive" -C "$tmp/x" ;;
  esac
  install -m 0755 "$tmp/x/$4" "/usr/local/bin/$1"
  rm -rf "$tmp"
}

say "nomad $NOMAD_VERSION"
if ! /usr/local/bin/nomad version 2>/dev/null | grep -q "v$NOMAD_VERSION"; then
  install_bin nomad "https://releases.hashicorp.com/nomad/$NOMAD_VERSION/nomad_${NOMAD_VERSION}_linux_amd64.zip" "$NOMAD_SHA256_LINUX_AMD64" nomad
fi

say "node $NODE_MAJOR (for Next.js apps) and python venv"
if ! node --version 2>/dev/null | grep -q "^v$NODE_MAJOR\."; then
  echo "  install Node $NODE_MAJOR from your distribution or nodejs.org, then run this again" >&2
fi
$PKG python3-pip >/dev/null 2>&1 || true

say "nomad config"
mkdir -p /etc/nomad.d /opt/nomad/data /srv/iima/backups
join=""
IFS=, read -ra servers <<<"$SERVER_IPS"
for s in "${servers[@]}"; do join+="\"$s\", "; done
IS_SERVER=false; [ "$ROLE" = server ] && IS_SERVER=true
IS_ENTRY="$ENTRY" NODE_NAME="$NODE_NAME" NODE_IP="$NODE_IP" NET_IFACE="$NET_IFACE" \
  IS_SERVER="$IS_SERVER" SERVER_JOIN="${join%, }" \
  envsubst '${NODE_NAME} ${NODE_IP} ${NET_IFACE} ${IS_SERVER} ${IS_ENTRY} ${SERVER_JOIN}' \
  <"$REPO/deploy/nomad/nomad.hcl.tmpl" >/etc/nomad.d/nomad.hcl
install -m 0644 "$REPO/deploy/servers/nomad.service" /etc/systemd/system/nomad.service

if [ "$ROLE" = server ]; then
  say "nomad snapshots (daily, 14 kept)"
  install -m 0755 "$REPO/deploy/servers/nomad-snapshot.sh" /usr/local/bin/nomad-snapshot
  install -m 0644 "$REPO/deploy/servers/nomad-snapshot.service" /etc/systemd/system/
  install -m 0644 "$REPO/deploy/servers/nomad-snapshot.timer" /etc/systemd/system/
  [ -f /etc/nomad.d/snapshot.token ] || echo "  put a management token in /etc/nomad.d/snapshot.token (mode 600) after bootstrapping ACLs" >&2
fi

if [ "$ENTRY" = true ]; then
  say "traefik $TRAEFIK_VERSION"
  if ! /usr/local/bin/traefik version 2>/dev/null | grep -q "${TRAEFIK_VERSION#v}"; then
    install_bin traefik "https://github.com/traefik/traefik/releases/download/$TRAEFIK_VERSION/traefik_${TRAEFIK_VERSION}_linux_amd64.tar.gz" "$TRAEFIK_SHA256_LINUX_AMD64" traefik
  fi
  id traefik >/dev/null 2>&1 || useradd --system --home /etc/traefik --shell /usr/sbin/nologin traefik
  mkdir -p /etc/traefik/dynamic /etc/traefik/certs /var/log/traefik
  install -m 0644 "$REPO/deploy/traefik/traefik.prod.yml" /etc/traefik/traefik.yml
  install -m 0644 "$REPO/deploy/traefik/dynamic/platform.yml" /etc/traefik/dynamic/platform.yml
  [ -f /etc/traefik/dynamic/tls.yml ] || echo "  copy deploy/traefik/dynamic/tls.prod.yml.example to /etc/traefik/dynamic/tls.yml and add the certificate" >&2
  for p in platformstrip proxyreverse; do
    plugin=/etc/traefik/plugins-local/src/github.com/arnav127/iima-platform/deploy/traefik/plugins/$p
    rm -rf "$plugin" && mkdir -p "$plugin" && cp -r "$REPO/deploy/traefik/plugins/$p/." "$plugin/"
  done
  if [ -f /etc/traefik/nomad.token ]; then
    sed -i "s|      token: \"\".*|      token: \"$(cat /etc/traefik/nomad.token)\"|" /etc/traefik/traefik.yml
  else
    echo "  put Traefik's Nomad token (policy traefik) in /etc/traefik/nomad.token and run this again" >&2
  fi
  chown -R traefik: /etc/traefik /var/log/traefik
  chmod 600 /etc/traefik/traefik.yml
  install -m 0644 "$REPO/deploy/servers/traefik.service" /etc/systemd/system/traefik.service
fi

say "firewall"
mkdir -p /etc/nftables.d
ENTRY_RULES=""
[ "$ENTRY" = true ] && ENTRY_RULES='tcp dport { 80, 443 } accept
    udp dport 443 accept'
CLUSTER_CIDR="$CLUSTER_CIDR" ENTRY_RULES="$ENTRY_RULES" envsubst '${CLUSTER_CIDR} ${ENTRY_RULES}' \
  <"$REPO/deploy/servers/iima.nft.tmpl" >/etc/nftables.d/iima.nft
if [ "${FIREWALL:-}" = apply ]; then
  nft -c -f /etc/nftables.d/iima.nft && nft -f /etc/nftables.d/iima.nft
  echo "  applied; make it permanent by including /etc/nftables.d/iima.nft from /etc/sysconfig/nftables.conf or /etc/nftables.conf"
else
  echo "  written to /etc/nftables.d/iima.nft; read it, then run again with FIREWALL=apply"
fi

say "services"
systemctl daemon-reload
systemctl enable --now nomad
systemctl restart nomad
[ "$ROLE" = server ] && systemctl enable --now nomad-snapshot.timer
if [ "$ENTRY" = true ] && [ -f /etc/traefik/dynamic/tls.yml ]; then
  systemctl enable --now traefik && systemctl restart traefik
fi

say "done on $NODE_NAME ($ROLE$([ "$ENTRY" = true ] && echo ', entry'))"

#!/usr/bin/env bash
# CMediaStack for Proxmox VE: creates an LXC container and installs it, in the
# manner of the community-scripts (tteck) helpers. Run in the Proxmox host's
# shell:
#
#   bash -c "$(curl -fsSL https://raw.githubusercontent.com/Jakethecake75/cmediastack/main/ct/cmediastack.sh)"
#
# Run the same line inside a CMediaStack container to update it.
#
# Environment: CMS_REPO=owner/repo and CMS_BRANCH=branch choose where the
# installer comes from (default Jakethecake75/cmediastack, main).
set -Eeuo pipefail

APP="CMediaStack"
REPO="${CMS_REPO:-Jakethecake75/cmediastack}"
BRANCH="${CMS_BRANCH:-main}"
RAW="https://raw.githubusercontent.com/$REPO/$BRANCH"

# Defaults. Media lives inside the container, so the disk is sized for it.
var_cpu=2
var_ram=2048
var_disk=64
var_hostname=cmediastack
var_bridge=vmbr0

msg() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok() { printf '\033[1;32m ok\033[0m %s\n' "$*"; }
die() {
  printf '\033[1;31mERR\033[0m %s\n' "$*" >&2
  exit 1
}
trap 'die "failed at line $LINENO: $BASH_COMMAND"' ERR

header_info() {
  clear 2>/dev/null || true
  cat <<'EOF'
   ________  ___         ___      _____ __             __
  / ____/  |/  /__  ____/ (_)___ / ___// /_____ ______/ /__
 / /   / /|_/ / _ \/ __  / / __ `\__ \/ __/ __ `/ ___/ //_/
/ /___/ /  / /  __/ /_/ / / /_/ /__/ / /_/ /_/ / /__/ ,<
\____/_/  /_/\___/\__,_/_/\__,_/____/\__/\__,_/\___/_/|_|

EOF
}

# Inside an existing CMediaStack container: update it.
if [ -x /opt/cmediastack/cmediastack ]; then
  header_info
  msg "Updating $APP in this container"
  bash -c "$(curl -fsSL "$RAW/install/cmediastack-install.sh")" cmediastack-install update
  exit 0
fi

command -v pveversion >/dev/null 2>&1 ||
  die "run this in the Proxmox VE host's shell (or inside a $APP container, to update it)"
[ "$(id -u)" -eq 0 ] || die "run this as root"
command -v whiptail >/dev/null 2>&1 || die "whiptail is missing: apt-get install whiptail"

header_info

ask() { # ask TITLE TEXT DEFAULT
  whiptail --title "$1" --inputbox "$2" 10 64 "$3" 3>&1 1>&2 2>&3 || die "cancelled"
}

# first_storage CONTENT: the first active storage that holds CONTENT.
first_storage() {
  pvesm status -content "$1" 2>/dev/null | awk 'NR > 1 && $3 == "active" { print $1; exit }'
}

pick_storage() { # pick_storage CONTENT TITLE
  local options=() name
  while read -r name; do
    options+=("$name" "")
  done < <(pvesm status -content "$1" 2>/dev/null | awk 'NR > 1 && $3 == "active" { print $1 }')
  [ "${#options[@]}" -gt 0 ] || die "no active storage holds $1"
  whiptail --title "$2" --menu "Where should it go?" 16 64 6 "${options[@]}" 3>&1 1>&2 2>&3 || die "cancelled"
}

CTID=$(pvesh get /cluster/nextid)
HN=$var_hostname
CPU=$var_cpu
RAM=$var_ram
DISK=$var_disk
BRIDGE=$var_bridge
NET="ip=dhcp"
ROOT_STORAGE=$(first_storage rootdir)
TMPL_STORAGE=$(first_storage vztmpl)

CHOICE=$(whiptail --title "$APP" --menu "How should the container be made?" 12 64 2 \
  "1" "Default settings ($CPU cores, ${RAM} MB, ${DISK} GB, DHCP on $BRIDGE)" \
  "2" "Advanced settings" 3>&1 1>&2 2>&3) || die "cancelled"

if [ "$CHOICE" = "2" ]; then
  CTID=$(ask "Container ID" "The container's ID" "$CTID")
  HN=$(ask "Hostname" "The container's hostname" "$HN")
  DISK=$(ask "Disk" "Disk size in GB. Your films, series, music and books live on it." "$DISK")
  CPU=$(ask "CPU" "CPU cores" "$CPU")
  RAM=$(ask "Memory" "Memory in MB" "$RAM")
  BRIDGE=$(ask "Network" "Bridge" "$BRIDGE")
  IP=$(ask "Address" "dhcp, or a fixed address with its prefix (e.g. 192.168.1.50/24)" "dhcp")
  if [ "$IP" != "dhcp" ]; then
    GW=$(ask "Gateway" "The gateway for $IP" "")
    NET="ip=$IP,gw=$GW"
  fi
  ROOT_STORAGE=$(pick_storage rootdir "Container disk")
  TMPL_STORAGE=$(pick_storage vztmpl "Template storage")
fi

[ -n "$ROOT_STORAGE" ] || die "no active storage holds container disks"
[ -n "$TMPL_STORAGE" ] || die "no active storage holds container templates"
case "$CTID$CPU$RAM$DISK" in *[!0-9]*) die "the ID, cores, memory and disk must be numbers" ;; esac
pct status "$CTID" >/dev/null 2>&1 && die "container $CTID already exists"

msg "Finding the Debian 12 template"
pveam update >/dev/null
TEMPLATE=$(pveam available --section system | awk '$2 ~ /^debian-12-standard_/ { print $2 }' | sort -V | tail -n1)
[ -n "$TEMPLATE" ] || die "no Debian 12 template is offered by pveam"
if ! pveam list "$TMPL_STORAGE" | grep -q "$TEMPLATE"; then
  msg "Downloading $TEMPLATE"
  pveam download "$TMPL_STORAGE" "$TEMPLATE" >/dev/null
fi
ok "$TEMPLATE"

msg "Creating container $CTID ($HN)"
# Unprivileged, with nesting: the media-parser sandbox creates user, PID and
# network namespaces (ADR-0020), which an unprivileged container allows only
# with nesting on.
pct create "$CTID" "$TMPL_STORAGE:vztmpl/$TEMPLATE" \
  --hostname "$HN" --cores "$CPU" --memory "$RAM" --swap 512 \
  --rootfs "$ROOT_STORAGE:$DISK" \
  --net0 "name=eth0,bridge=$BRIDGE,$NET" \
  --unprivileged 1 --features nesting=1 --onboot 1 --ostype debian \
  --description "$APP — https://github.com/$REPO" >/dev/null
pct start "$CTID"
ok "started"

msg "Waiting for the network"
ADDR=""
for _ in $(seq 1 60); do
  ADDR=$(pct exec "$CTID" -- ip -4 route get 1.1.1.1 2>/dev/null |
    awk '{for (i = 1; i <= NF; i++) if ($i == "src") { print $(i + 1); exit }}') || true
  if [ -n "$ADDR" ] && pct exec "$CTID" -- getent hosts github.com >/dev/null 2>&1; then break; fi
  ADDR=""
  sleep 1
done
[ -n "$ADDR" ] || die "container $CTID has no working network after a minute (address and DNS)"
ok "$ADDR"

msg "Installing $APP inside the container"
pct exec "$CTID" -- bash -c "$(curl -fsSL "$RAW/install/cmediastack-install.sh")" cmediastack-install install

cat <<EOF

  $APP is running in container $CTID.

  Open  https://$ADDR:8443/setup  now: whoever opens it first becomes the only
  administrator. The certificate is self-signed, so the browser warns once.
  After signing in, Getting started walks through TMDB, OpenSubtitles, a SOCKS5
  proxy, the libraries, an indexer and Discord.

  The master key — copy it somewhere that is not this container:
    pct exec $CTID -- cat /etc/cmediastack/cmediastack.env

  To update later, run this same command inside the container
  (pct enter $CTID).

EOF

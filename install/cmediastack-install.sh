#!/usr/bin/env bash
# CMediaStack installer, run INSIDE a Debian 12 LXC container as root.
#
# ct/cmediastack.sh runs it after creating the container; it can also be run by
# hand in any Debian 12 machine or container:
#
#   bash -c "$(curl -fsSL https://raw.githubusercontent.com/Jakethecake75/cmediastack/main/install/cmediastack-install.sh)"
#
# With the argument `update` it replaces the binary with the latest release
# instead, and puts the old one back if the new one does not come up.
#
# Environment, for testing:
#   CMS_BINARY=/path   install this binary instead of downloading a release
#   CMS_REPO=owner/repo  the GitHub repository releases come from
set -Eeuo pipefail

REPO="${CMS_REPO:-Jakethecake75/cmediastack}"
APP_DIR=/opt/cmediastack
BIN="$APP_DIR/cmediastack"
ETC=/etc/cmediastack
DATA=/var/lib/cmediastack
MEDIA=/media
PORT=8443
READY=http://127.0.0.1:9090/readyz

msg() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok() { printf '\033[1;32m ok\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m !!\033[0m %s\n' "$*"; }
die() {
  printf '\033[1;31mERR\033[0m %s\n' "$*" >&2
  exit 1
}
trap 'die "failed at line $LINENO: $BASH_COMMAND"' ERR

[ "$(id -u)" -eq 0 ] || die "run this as root"

# primary_ip is the address other machines reach this one at.
primary_ip() {
  ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i <= NF; i++) if ($i == "src") { print $(i + 1); exit }}'
}

arch() {
  case "$(dpkg --print-architecture)" in
  amd64) echo amd64 ;;
  arm64) echo arm64 ;;
  *) die "no release is built for $(dpkg --print-architecture)" ;;
  esac
}

# fetch_release DEST: the latest release's binary for this architecture,
# verified against the release's SHA256SUMS.
fetch_release() {
  local dest=$1 a tag tmp
  if [ -n "${CMS_BINARY:-}" ]; then
    msg "Installing the local binary $CMS_BINARY"
    install -m 0755 "$CMS_BINARY" "$dest"
    return
  fi
  a=$(arch)
  tag=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
    sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)
  [ -n "$tag" ] || die "could not find the latest release of $REPO"
  msg "Downloading CMediaStack $tag ($a)"
  tmp=$(mktemp -d)
  curl -fsSL -o "$tmp/cmediastack-linux-$a" "https://github.com/$REPO/releases/download/$tag/cmediastack-linux-$a"
  curl -fsSL -o "$tmp/SHA256SUMS" "https://github.com/$REPO/releases/download/$tag/SHA256SUMS"
  (cd "$tmp" && sha256sum -c --ignore-missing --status SHA256SUMS) || die "the download does not match the release's SHA256SUMS"
  ok "checksum verified"
  install -m 0755 "$tmp/cmediastack-linux-$a" "$dest"
  rm -rf "$tmp"
}

# write_tls IP HOST: a self-signed certificate for the address and the name.
# The app marks its session cookie Secure unless the base URL is
# http://localhost, so plain HTTP on a LAN address cannot sign in at all.
write_tls() {
  local ip=$1 host=$2
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 3650 \
    -subj "/CN=$host" -addext "subjectAltName=IP:$ip,DNS:$host,DNS:localhost" \
    -keyout "$ETC/tls.key" -out "$ETC/tls.crt" 2>/dev/null
  chown root:cmediastack "$ETC/tls.key" "$ETC/tls.crt"
  chmod 0640 "$ETC/tls.key"
  chmod 0644 "$ETC/tls.crt"
}

write_config() {
  local ip=$1
  cat >"$ETC/config.yaml" <<EOF
# CMediaStack, as installed by install/cmediastack-install.sh. Secrets are not
# here: the master key is in cmediastack.env, and every login and key is set
# on the web page (Getting started).
server:
  addr: "0.0.0.0:$PORT"
  management_addr: "127.0.0.1:9090"
  base_url: "https://$ip:$PORT"
  tls_cert_file: "$ETC/tls.crt"
  tls_key_file: "$ETC/tls.key"
database:
  path: "$DATA/cmediastack.db"
egress:
  # No WireGuard namespace in this container. A SOCKS5 proxy set on the
  # Network tab is its egress control (ADR-0065); until one is set, downloads
  # leave by this container's own address.
  anonymity_enabled: false
  require_namespace_guard: false
download:
  enabled: true
  data_dir: "$MEDIA/downloads"
acquisition:
  # Adding a title searches for it and grabs the best release (ADR-0071).
  automatic: true
EOF
  chown root:cmediastack "$ETC/config.yaml"
  chmod 0640 "$ETC/config.yaml"
}

# ensure_address: the base URL and the certificate name this container's
# current address. A DHCP lease that changed would otherwise break sign-in.
ensure_address() {
  local ip host current
  ip=$(primary_ip)
  host=$(hostname)
  [ -n "$ip" ] || die "this container has no IPv4 address"
  current=$(sed -n 's#.*base_url: "https://\([^:"]*\):.*#\1#p' "$ETC/config.yaml")
  if [ "$current" != "$ip" ]; then
    warn "the address changed from ${current:-nothing} to $ip: updating the base URL and the certificate"
    sed -i "s#base_url: \"https://[^\"]*\"#base_url: \"https://$ip:$PORT\"#" "$ETC/config.yaml"
    write_tls "$ip" "$host"
    return 0
  fi
  return 1
}

# ensure_acquisition: automatic acquisition on where the configuration does not
# say either way (ADR-0071). An operator who set it, on or off, is left alone.
ensure_acquisition() {
  grep -q '^acquisition:' "$ETC/config.yaml" && return 1
  printf '%s\n' 'acquisition:' \
    '  # Adding a title searches for it and grabs the best release (ADR-0071).' \
    '  automatic: true' >>"$ETC/config.yaml"
  ok "automatic acquisition is on: adding a title now searches for it"
}

install_service() {
  cat >/etc/systemd/system/cmediastack.service <<EOF
[Unit]
Description=CMediaStack
Wants=network-online.target
After=network-online.target

[Service]
User=cmediastack
Group=cmediastack
EnvironmentFile=$ETC/cmediastack.env
ExecStart=$BIN --config $ETC/config.yaml
# Exit status 3 is a restart asked for from the web (ADR-0065).
Restart=always
RestartSec=2
TimeoutStopSec=30
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=$DATA $MEDIA
ProtectHome=yes
PrivateTmp=yes
CapabilityBoundingSet=
# Not RestrictNamespaces or PrivateUsers: the media-parser jail creates user,
# PID and network namespaces (ADR-0020).

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
}

wait_ready() {
  local tries=0
  until curl -fsS -o /dev/null "$READY" 2>/dev/null; do
    tries=$((tries + 1))
    [ "$tries" -lt 60 ] || return 1
    sleep 1
  done
}

check_sandbox() {
  if journalctl -u cmediastack -b --no-pager 2>/dev/null | grep "media parser sandbox is available" >/dev/null; then
    ok "the media-parser sandbox is available"
  else
    warn "the media-parser sandbox is NOT available: ffprobe would read untrusted files with no"
    warn "network isolation. In Proxmox the usual cause is the container lacking nesting=1:"
    warn "  pct set <id> --features nesting=1   (then restart the container)"
  fi
}

update() {
  [ -x "$BIN" ] || die "CMediaStack is not installed here"
  local before after
  before=$("$BIN" -version 2>/dev/null || echo unknown)
  fetch_release "$BIN.new"
  after=$("$BIN.new" -version 2>/dev/null || echo unknown)
  if [ "$before" = "$after" ] && [ -z "${CMS_BINARY:-}" ]; then
    rm -f "$BIN.new"
    ok "already at $before"
    local changed=1
    ensure_acquisition && changed=0
    ensure_address && changed=0
    [ "$changed" -eq 0 ] && systemctl restart cmediastack
    return 0
  fi
  msg "Updating $before to $after"
  systemctl stop cmediastack
  cp -p "$BIN" "$BIN.old"
  mv "$BIN.new" "$BIN"
  ensure_acquisition || true
  ensure_address || true
  systemctl start cmediastack
  if wait_ready; then
    ok "updated to $after"
  else
    warn "the new version did not come up within a minute: putting $before back"
    systemctl stop cmediastack || true
    mv "$BIN.old" "$BIN"
    systemctl start cmediastack
    wait_ready || die "the old version did not come back either: see journalctl -u cmediastack"
    die "the update failed and was rolled back; see journalctl -u cmediastack for why"
  fi
}

install_fresh() {
  msg "Installing what it needs"
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq ffmpeg curl ca-certificates openssl >/dev/null
  ok "ffmpeg, curl, openssl"

  id cmediastack >/dev/null 2>&1 ||
    useradd --system --user-group --no-create-home --home-dir "$DATA" --shell /usr/sbin/nologin cmediastack
  install -d -m 0755 "$APP_DIR"
  install -d -m 0750 -o root -g cmediastack "$ETC"
  install -d -m 0700 -o cmediastack -g cmediastack "$DATA"
  install -d -m 0755 -o cmediastack -g cmediastack "$MEDIA"
  for d in movies tv music books downloads; do
    install -d -m 0755 -o cmediastack -g cmediastack "$MEDIA/$d"
  done

  fetch_release "$BIN"

  if [ ! -s "$ETC/cmediastack.env" ]; then
    (umask 077 && printf 'CMS_MASTER_KEY=%s\n' "$(head -c32 /dev/urandom | base64)" >"$ETC/cmediastack.env")
    chown root:cmediastack "$ETC/cmediastack.env"
    chmod 0640 "$ETC/cmediastack.env"
    ok "generated the master key"
  fi

  local ip
  ip=$(primary_ip)
  [ -n "$ip" ] || die "this container has no IPv4 address"
  [ -f "$ETC/config.yaml" ] || write_config "$ip"
  [ -f "$ETC/tls.crt" ] || write_tls "$ip" "$(hostname)"
  ensure_address || true

  install_service
  systemctl enable -q --now cmediastack
  wait_ready || die "CMediaStack did not come up within a minute: see journalctl -u cmediastack"
  ok "CMediaStack is running"
  check_sandbox

  cat <<EOF

  CMediaStack is ready:  https://$ip:$PORT/setup

  - The certificate is self-signed, so the browser warns once.
  - Whoever opens /setup first becomes the only administrator: open it now.
  - Copy the master key somewhere that is not this container. It unlocks every
    stored credential and every backup, and nothing can recover it:
      cat $ETC/cmediastack.env
  - Media lives under $MEDIA in this container. Give the container a fixed
    address (or a DHCP reservation); if it changes, run the update to follow it.

EOF
}

case "${1:-install}" in
install) install_fresh ;;
update) update ;;
*) die "usage: $0 [install|update]" ;;
esac

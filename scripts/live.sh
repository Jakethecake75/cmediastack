#!/usr/bin/env bash
# Runs a throwaway CMediaStack instance for the design screenshots, inside WSL.
# Every start is a fresh database, so setup and enrollment can be captured.
# Usage (from Windows): wsl.exe -d Ubuntu-24.04 -- bash scripts/live.sh <binary> <port>
#                       wsl.exe -d Ubuntu-24.04 -- bash scripts/live.sh stop
set -eu
DIR=/tmp/cms-design
if [ "$1" = stop ]; then
  kill $(pgrep -f "$DIR/config.yaml") 2>/dev/null || true
  while pgrep -f "$DIR/config.yaml" >/dev/null; do sleep 0.2; done
  exit 0
fi
BIN="$1"; PORT="${2:-8090}"
rm -rf "$DIR"; mkdir -p "$DIR/films" "$DIR/tv"
cp "$BIN" "$DIR/cms"; chmod +x "$DIR/cms"
cat > "$DIR/config.yaml" <<EOF
server:
  addr: "127.0.0.1:$PORT"
  management_addr: "127.0.0.1:$((PORT + 1000))"
  base_url: "http://127.0.0.1:$PORT"
database:
  path: "$DIR/cms.db"
auth:
  breach_check_enabled: false
registration:
  mode: "open"
EOF
export CMS_MASTER_KEY="$(head -c32 /dev/urandom | base64)"
exec "$DIR/cms" -config "$DIR/config.yaml" >"$DIR/server.log" 2>&1

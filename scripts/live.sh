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
# A small library for the screens to show: sparse files over the 8 MiB
# sample threshold, named the way a scan expects.
for t in "The Long Way Home (2019)" "Northern Lights (2021)" "Paper Moon Harbour (1998)"          "A Quiet Engine (2024)" "Salt and Ember (2016)" "The Cartographer (2011)"; do
  mkdir -p "$DIR/films/$t"; truncate -s 20M "$DIR/films/$t/$t.mkv"
done
for t in "Harbour Lights (2022)" "Field Notes (2019)"; do
  for e in 01 02 03; do
    mkdir -p "$DIR/tv/$t/Season 01"; truncate -s 20M "$DIR/tv/$t/Season 01/${t% (*} - S01E$e.mkv"
  done
done
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

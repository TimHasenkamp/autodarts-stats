#!/usr/bin/env bash
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
P="$(cd "$HERE/../.." && pwd)"
S="$(mktemp -d)"
trap 'rm -rf "$S"' EXIT
PORT_T=18100
# Altlasten auf dem Testport beenden (NIE Port 8080 anfassen)
for pid in $(ss -lntp 2>/dev/null | grep ":$PORT_T " | grep -oE 'pid=[0-9]+' | cut -d= -f2 | sort -u); do
  echo "beende alten Testserver PID $pid auf Port $PORT_T"
  kill "$pid" 2>/dev/null
done
sleep 0.5
rm -f "$S"/t.db* "$S"/srv.log
cd "$P" || exit 1
KEY=$(DB_PATH="$S/t.db" ./autodarts-stats board add "Testboard" | grep -o 'adb_[A-Za-z0-9_-]*')
echo "Key erzeugt: ${KEY:0:10}…"
DB_PATH="$S/t.db" PORT=$PORT_T ADMIN_PASSWORD=x MAX_UNPARSED=5000 ./autodarts-stats serve > "$S/srv.log" 2>&1 &
SRVPID=$!
sleep 1.5
cd "$HERE" || exit 1
SRV_PORT=$PORT_T BOARD_KEY="$KEY" PROJECT_DIR="$P" timeout 180 node "$HERE/test.mjs"
EC=$?
kill $SRVPID 2>/dev/null
wait $SRVPID 2>/dev/null
sleep 0.3
echo "--- exit=$EC"
echo "--- Serverlog (Ingest/Ping):"
grep -E "api/ingest" "$S/srv.log" | head -6
exit $EC

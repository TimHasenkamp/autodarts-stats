#!/usr/bin/env bash
# Baut das Frontend (embedded ins Go-Binary) und startet die Playwright-Tests.
# Der Go-Server wird von global-setup.mjs gebaut/gestartet.
# SKIP_WEB_BUILD=1 ueberspringt den Frontend-Build.
set -eu
HERE="$(cd "$(dirname "$0")" && pwd)"
P="$(cd "$HERE/../.." && pwd)"

if [ "${SKIP_WEB_BUILD:-0}" != "1" ]; then
  echo "== Frontend bauen"
  (cd "$P/web" && npm run build)
  # Der Adapter leert dist; Platzhalter fuer //go:embed wiederherstellen (wie Makefile: dist-placeholder)
  mkdir -p "$P/server/internal/web/dist"
  (cd "$P" && git checkout -- server/internal/web/dist/.gitkeep 2>/dev/null || touch server/internal/web/dist/.gitkeep)
fi

cd "$HERE"
[ -d node_modules ] || npm install
npx playwright install chromium >/dev/null 2>&1 || true
npx playwright test "$@"

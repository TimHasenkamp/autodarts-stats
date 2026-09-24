#!/bin/sh
# Laufende Agenten der angemeldeten Benutzer nach einem Update neu starten.
set -e
for dir in /run/user/*; do
  [ -S "$dir/bus" ] || continue
  uid=$(basename "$dir")
  user=$(getent passwd "$uid" | cut -d: -f1) || continue
  [ -n "$user" ] || continue
  su -s /bin/sh "$user" -c "export XDG_RUNTIME_DIR=$dir; systemctl --user daemon-reload; systemctl --user try-restart autodarts-stats-agent.service" >/dev/null 2>&1 || true
done

cat <<'EOF'

Your Darts Agent installiert. Einrichtung (als Benutzer am Board, ohne sudo):
  mkdir -p ~/.config
  cp /usr/share/your-darts-agent/agent.env.example ~/.config/autodarts-stats-agent.env
  nano ~/.config/autodarts-stats-agent.env        # BACKEND_URL, API_KEY, READER
  systemctl --user enable --now autodarts-stats-agent
Mehr: /usr/share/doc/your-darts-agent/README

EOF
exit 0

#!/usr/bin/env bash
# Runs on the Pi (as root, via deploy.ps1). Usage:
#   install.sh install            first-time setup: user, folders, service, config
#   install.sh update [--config]  replace the binary (and config with --config), restart
#   install.sh rollback           go back to the previous binary
set -euo pipefail

APP=/opt/netguard
SRC="$(cd "$(dirname "$0")" && pwd)"
MODE="${1:-update}"
PUSH_CONFIG="${2:-}"

install_config() {
  if [ -f "$APP/config.yaml" ]; then
    cp -p "$APP/config.yaml" "$APP/config.yaml.bak"
  fi
  # 0640: the config holds the dashboard password.
  install -m 0640 -o root -g netguard "$SRC/config.yaml" "$APP/config.yaml"
}

install_service() {
  if ! cmp -s "$SRC/netguard.service" /etc/systemd/system/netguard.service 2>/dev/null; then
    install -m 0644 "$SRC/netguard.service" /etc/systemd/system/netguard.service
    systemctl daemon-reload
  fi
}

case "$MODE" in
  install)
    id netguard &>/dev/null || useradd --system --home-dir "$APP" --shell /usr/sbin/nologin netguard
    mkdir -p "$APP/data"
    chown -R netguard:netguard "$APP/data"
    install -m 0755 "$SRC/netguard" "$APP/netguard"
    install -m 0755 "$SRC/install.sh" "$APP/install.sh"
    if [ -f "$SRC/config.yaml" ] && { [ ! -f "$APP/config.yaml" ] || [ "$PUSH_CONFIG" = "--config" ]; }; then
      install_config
    fi
    install_service
    systemctl enable netguard
    systemctl restart netguard
    ;;
  update)
    [ -f "$APP/netguard" ] && cp -p "$APP/netguard" "$APP/netguard.prev"
    install -m 0755 "$SRC/netguard" "$APP/netguard.new"
    mv -f "$APP/netguard.new" "$APP/netguard"
    install -m 0755 "$SRC/install.sh" "$APP/install.sh"
    if [ "$PUSH_CONFIG" = "--config" ]; then
      install_config
    fi
    install_service
    systemctl restart netguard
    ;;
  rollback)
    if [ ! -f "$APP/netguard.prev" ]; then
      echo "No previous version to roll back to." >&2
      exit 1
    fi
    cp -p "$APP/netguard.prev" "$APP/netguard"
    systemctl restart netguard
    ;;
  *)
    echo "usage: $0 install|update [--config]|rollback" >&2
    exit 2
    ;;
esac

sleep 2
if systemctl is-active --quiet netguard; then
  echo "OK: NetGuard $("$APP/netguard" -version) is running."
else
  echo "NetGuard failed to start. Last log lines:" >&2
  journalctl -u netguard -n 30 --no-pager >&2
  echo "Undo with: deploy.ps1 -Rollback" >&2
  exit 1
fi

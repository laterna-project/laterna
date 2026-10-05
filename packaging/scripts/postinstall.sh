#!/bin/sh
# Runs after the package is installed or upgraded.
# deb: $1 is "configure" and $2 the previous version (empty on a first install).
# rpm: $1 is 1 on install and 2 on upgrade.
set -e

upgrade=no
case "${1:-}" in
  configure) [ -n "${2:-}" ] && upgrade=yes ;;
  2) upgrade=yes ;;
esac

if command -v systemd-sysusers >/dev/null 2>&1; then
  systemd-sysusers /usr/lib/sysusers.d/laterna.conf
else
  getent group laterna >/dev/null || groupadd --system laterna
  getent passwd laterna >/dev/null ||
    useradd --system --gid laterna --home-dir /var/lib/laterna --shell /usr/sbin/nologin laterna
fi

if [ -d /run/systemd/system ]; then
  systemctl daemon-reload
  if [ "$upgrade" = yes ]; then
    systemctl try-restart laterna.service
  else
    systemctl enable --now laterna.service
  fi
fi

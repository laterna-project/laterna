#!/bin/sh
# Runs before the package is removed or upgraded. The service is only stopped on removal.
# deb: $1 is "remove" or "upgrade". rpm: $1 is 0 on removal and 1 on upgrade.
set -e

case "${1:-}" in
  remove | 0)
    if [ -d /run/systemd/system ]; then
      systemctl disable --now laterna.service || true
    fi
    ;;
esac

#!/bin/sh
# Runs after the package is removed. The data in /var/lib/laterna is left in place.
set -e

if [ -d /run/systemd/system ]; then
  systemctl daemon-reload || true
fi

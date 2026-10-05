#!/bin/sh
# Tries what a release ships, on a Debian or Ubuntu amd64 machine with systemd (the CI runner):
# the archive with FFmpeg, then the deb package and its service.
#
#   packaging/test-release.sh <dist folder>
set -eu

dist=${1:?usage: test-release.sh <dist folder>}

wait_for() {
  i=0
  until curl -fsS "$1/health" >/dev/null 2>&1; do
    i=$((i + 1))
    if [ "$i" -gt 30 ]; then
      echo "no answer from $1 within 30 s" >&2
      return 1
    fi
    sleep 1
  done
}

echo "== what was built"
ls -l "$dist"/*.tar.gz "$dist"/*.tar.xz "$dist"/*.zip "$dist"/*.deb "$dist"/*.rpm "$dist"/checksums.txt
(cd "$dist" && sha256sum --check --quiet checksums.txt)

echo "== archive with FFmpeg"
work=$(mktemp -d)
pid=
trap 'kill "$pid" 2>/dev/null || true; rm -rf "$work"' EXIT
tar -xJf "$dist"/laterna_*_linux_amd64_ffmpeg.tar.xz -C "$work"
bundle=$(echo "$work"/laterna_*)
ls -l "$bundle"
"$bundle/laterna" version
# Nothing in PATH: the server must find the ffmpeg and ffprobe next to it.
env -i HOME="$work" PATH=/nonexistent \
  LATERNA_ADDRESS=127.0.0.1:18097 LATERNA_DATA_DIR="$work/data" LATERNA_CACHE_DIR="$work/cache" \
  LATERNA_METADATA_DIR="$work/metadata" LATERNA_DISCOVERY=false \
  "$bundle/laterna" serve >"$work/serve.log" 2>&1 &
pid=$!
wait_for http://127.0.0.1:18097 || { cat "$work/serve.log"; exit 1; }
sleep 3 # time for the startup check of ffprobe to be logged
kill "$pid"
wait "$pid" 2>/dev/null || true
pid=
cat "$work/serve.log"
if grep -q 'FFmpeg not found' "$work/serve.log"; then
  echo "the server did not find the bundled FFmpeg" >&2
  exit 1
fi

echo "== deb package"
deb=$(echo "$dist"/laterna_*_amd64.deb)
if [ ! -f "$deb" ]; then
  echo "no deb package for amd64 in $dist" >&2
  exit 1
fi
dpkg-deb --info "$deb"
dpkg-deb --contents "$deb"
sudo apt-get install -y "./$deb"
wait_for http://127.0.0.1:8096 || { sudo journalctl -u laterna --no-pager | tail -n 50; exit 1; }
systemctl is-active laterna
systemctl is-enabled laterna
/usr/lib/laterna/ffmpeg -hide_banner -version | head -n 1
curl -fsS -X POST -H 'Content-Type: application/json' -d '{}' \
  http://127.0.0.1:8096/laterna.v1.ServerService/GetServerInfo
echo
sudo journalctl -u laterna --no-pager | tail -n 20
if sudo journalctl -u laterna --no-pager | grep -q 'FFmpeg not found'; then
  echo "the packaged server did not find its FFmpeg" >&2
  exit 1
fi
sudo apt-get remove -y laterna
if systemctl is-active --quiet laterna; then
  echo "the service still runs after the package was removed" >&2
  exit 1
fi

echo "== rpm package"
rpm=$(echo "$dist"/laterna-*.x86_64.rpm)
if [ ! -f "$rpm" ]; then
  echo "no rpm package for x86_64 in $dist" >&2
  exit 1
fi
if command -v rpm >/dev/null 2>&1; then
  rpm -qip "$rpm"
  rpm -qlp "$rpm"
fi
echo "release ok"

#!/bin/sh
# Downloads the pinned web client release (packaging/web.lock), checks its SHA-256 and unpacks it
# in a folder, ready for paths.web (LATERNA_WEB_DIR).
#
#   packaging/fetch-web.sh <destination>
set -eu

here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=packaging/web.lock
. "$here/web.lock"

if [ $# -ne 1 ]; then
  echo "usage: $0 <destination>" >&2
  exit 2
fi
dest=$1

name="laterna-web-${WEB_VERSION}"
url="https://github.com/laterna-project/laterna-web/releases/download/v${WEB_VERSION}/${name}.tar.gz"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "fetching $name.tar.gz"
curl -fsSL --retry 3 -o "$tmp/web.tar.gz" "$url"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(cd "$tmp" && sha256sum web.tar.gz | cut -d' ' -f1)
else
  actual=$(cd "$tmp" && shasum -a 256 web.tar.gz | cut -d' ' -f1)
fi
if [ "$actual" != "$WEB_SHA256" ]; then
  echo "checksum mismatch for $name.tar.gz: got $actual, want $WEB_SHA256" >&2
  exit 1
fi

tar -xzf "$tmp/web.tar.gz" -C "$tmp"
if [ ! -f "$tmp/$name/index.html" ]; then
  echo "$name.tar.gz holds no index.html" >&2
  exit 1
fi
mkdir -p "$dest"
cp -R "$tmp/$name/." "$dest/"
echo "web client $WEB_VERSION is in $dest"

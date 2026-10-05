#!/bin/sh
# Pins another web client release in packaging/web.lock, with the SHA-256 published next to it.
#
#   packaging/update-web.sh <version>      e.g. 0.2.0
set -eu

if [ $# -ne 1 ]; then
  echo "usage: $0 <version>" >&2
  exit 2
fi
version=${1#v}
here=$(cd "$(dirname "$0")" && pwd)
url="https://github.com/laterna-project/laterna-web/releases/download/v${version}/checksums.txt"

sum=$(curl -fsSL --retry 3 "$url" | awk -v f="laterna-web-${version}.tar.gz" '$2 == f { print $1 }')
if [ -z "$sum" ]; then
  echo "no laterna-web-${version}.tar.gz in $url" >&2
  exit 1
fi
sed -i.bak -e "s/^WEB_VERSION=.*/WEB_VERSION=${version}/" -e "s/^WEB_SHA256=.*/WEB_SHA256=${sum}/" "$here/web.lock"
rm -f "$here/web.lock.bak"
echo "web client pinned to $version ($sum)"

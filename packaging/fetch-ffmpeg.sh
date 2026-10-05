#!/bin/sh
# Downloads the pinned FFmpeg build (packaging/ffmpeg.lock), checks its SHA-256 and puts ffmpeg
# and ffprobe in a folder.
#
#   packaging/fetch-ffmpeg.sh <linux|windows> <amd64|arm64> <destination>
set -eu

here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=packaging/ffmpeg.lock
. "$here/ffmpeg.lock"

if [ $# -ne 3 ]; then
  echo "usage: $0 <linux|windows> <amd64|arm64> <destination>" >&2
  exit 2
fi
os=$1
arch=$2
dest=$3

case "$os/$arch" in
  linux/amd64) target=linux64 sum=$FFMPEG_SHA256_LINUX64 ext=tar.xz exe= ;;
  linux/arm64) target=linuxarm64 sum=$FFMPEG_SHA256_LINUXARM64 ext=tar.xz exe= ;;
  windows/amd64) target=win64 sum=$FFMPEG_SHA256_WIN64 ext=zip exe=.exe ;;
  windows/arm64) target=winarm64 sum=$FFMPEG_SHA256_WINARM64 ext=zip exe=.exe ;;
  *)
    echo "no pinned FFmpeg build for $os/$arch" >&2
    exit 1
    ;;
esac

name="${FFMPEG_NAME}-${target}-${FFMPEG_VARIANT}"
url="https://github.com/BtbN/FFmpeg-Builds/releases/download/${FFMPEG_RELEASE}/${name}.${ext}"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "fetching $name.$ext"
curl -fsSL --retry 3 -o "$tmp/ffmpeg.$ext" "$url"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(cd "$tmp" && sha256sum "ffmpeg.$ext" | cut -d' ' -f1)
else
  actual=$(cd "$tmp" && shasum -a 256 "ffmpeg.$ext" | cut -d' ' -f1)
fi
if [ "$actual" != "$sum" ]; then
  echo "checksum mismatch for $name.$ext: got $actual, want $sum" >&2
  exit 1
fi

case "$ext" in
  tar.xz) tar -xJf "$tmp/ffmpeg.$ext" -C "$tmp" ;;
  zip)
    if command -v unzip >/dev/null 2>&1; then
      unzip -q "$tmp/ffmpeg.$ext" -d "$tmp"
    else
      tar -xf "$tmp/ffmpeg.$ext" -C "$tmp"
    fi
    ;;
esac

mkdir -p "$dest"
cp "$tmp/$name/bin/ffmpeg$exe" "$tmp/$name/bin/ffprobe$exe" "$dest/"
if [ -f "$tmp/$name/LICENSE.txt" ]; then
  cp "$tmp/$name/LICENSE.txt" "$dest/FFMPEG-LICENSE.txt"
fi
echo "ffmpeg and ffprobe are in $dest"

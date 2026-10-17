#!/bin/sh
# Copies the FFmpeg builds pinned by the given lock files to the ffmpeg-builds pre-release of this
# repository (FFMPEG_MIRROR), where packaging/fetch-ffmpeg.sh looks first: BtbN deletes its daily
# builds after about two weeks. Each file is checked against its SHA-256 before it is uploaded.
# Run by the FFmpeg mirror workflow; needs gh and a token that may write releases.
#
#   packaging/mirror-ffmpeg.sh <ffmpeg.lock>...
set -eu

tag=ffmpeg-builds
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

if ! gh release view "$tag" >/dev/null 2>&1; then
  # The tag goes on the first commit, so that git describe --tags never prefers it to a version.
  root=$(git rev-list --max-parents=0 HEAD | tail -n 1)
  gh release create "$tag" --prerelease --latest=false --target "$root" --title "FFmpeg builds" \
    --notes "Unchanged copies of the FFmpeg builds pinned in \`packaging/ffmpeg.lock\`, taken from
[BtbN/FFmpeg-Builds](https://github.com/BtbN/FFmpeg-Builds), which deletes its daily builds after
about two weeks. \`packaging/fetch-ffmpeg.sh\` downloads them from here first and checks each one
against its SHA-256. This is not a Laterna release.

FFmpeg is free software under the GPL, and each archive holds its license. The source of a build
is the FFmpeg commit in its name (https://git.ffmpeg.org/ffmpeg.git), built with the scripts of
BtbN/FFmpeg-Builds."
fi
gh release view "$tag" --json assets --jq '.assets[].name' >"$tmp/assets"

for lock in "$@"; do
  # One subshell per lock: they all set the same variables.
  (
    # shellcheck source=packaging/ffmpeg.lock
    . "$lock"
    for target in linux64 linuxarm64 win64 winarm64; do
      case "$target" in
        linux64) sum=$FFMPEG_SHA256_LINUX64 ext=tar.xz ;;
        linuxarm64) sum=$FFMPEG_SHA256_LINUXARM64 ext=tar.xz ;;
        win64) sum=$FFMPEG_SHA256_WIN64 ext=zip ;;
        winarm64) sum=$FFMPEG_SHA256_WINARM64 ext=zip ;;
      esac
      name="${FFMPEG_NAME}-${target}-${FFMPEG_VARIANT}.${ext}"
      file="${FFMPEG_RELEASE}_${name}"
      if grep -qxF "$file" "$tmp/assets"; then
        echo "$file is already there"
        continue
      fi
      echo "copying $file"
      curl -fsSL --retry 3 -o "$tmp/$file" \
        "https://github.com/BtbN/FFmpeg-Builds/releases/download/${FFMPEG_RELEASE}/${name}"
      actual=$(sha256sum "$tmp/$file" | cut -d' ' -f1)
      if [ "$actual" != "$sum" ]; then
        echo "checksum mismatch for $name: got $actual, want $sum" >&2
        exit 1
      fi
      gh release upload "$tag" "$tmp/$file"
      echo "$file" >>"$tmp/assets"
      rm "$tmp/$file"
    done
  )
done

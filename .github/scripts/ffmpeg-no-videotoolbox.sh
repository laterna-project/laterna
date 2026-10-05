#!/bin/sh
# Stands in for ffmpeg on the hosted macOS runners. They announce the VideoToolbox encoders, but
# those hang or fail in that virtual machine. This hides them from "ffmpeg -encoders", which is
# where the server learns what to try; every other call goes to the real ffmpeg ($REAL_FFMPEG).
set -eu

for arg in "$@"; do
  if [ "$arg" = "-encoders" ]; then
    "$REAL_FFMPEG" "$@" | grep -v videotoolbox
    exit 0
  fi
done
exec "$REAL_FFMPEG" "$@"

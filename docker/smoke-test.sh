#!/bin/sh
# Starts the image, waits for the server to answer and checks that FFmpeg works inside.
#
#   docker/smoke-test.sh <image>
set -eu

image=${1:?usage: smoke-test.sh <image>}
name=laterna-smoke
url=http://127.0.0.1:18096

cleanup() {
  docker logs "$name" 2>&1 | tail -n 30 || true
  docker rm -f "$name" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run -d --name "$name" -p 127.0.0.1:18096:8096 "$image" >/dev/null

i=0
until curl -fsS "$url/health" >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -gt 30 ]; then
    echo "the server did not answer within 30 s" >&2
    exit 1
  fi
  sleep 1
done

curl -fsS -X POST -H 'Content-Type: application/json' -d '{}' "$url/laterna.v1.ServerService/GetServerInfo"
echo
docker exec "$name" laterna version
docker exec "$name" ffmpeg -hide_banner -version | head -n 1
docker exec "$name" ffprobe -hide_banner -version | head -n 1
# libx264 is the encoder of last resort: the image is useless without it.
docker exec "$name" ffmpeg -hide_banner -encoders | grep -q libx264
echo "image ok"

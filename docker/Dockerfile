# syntax=docker/dockerfile:1.7

# Build stage: runs on the build machine architecture and cross-compiles (no cgo).
ARG GO_VERSION=1.27
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETOS TARGETARCH VERSION=dev COMMIT=""
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false \
      -ldflags "-s -w -X github.com/laterna-project/laterna/internal/buildinfo.Version=${VERSION} -X github.com/laterna-project/laterna/internal/buildinfo.commit=${COMMIT}" \
      -o /out/laterna ./cmd/laterna

# Stock FFmpeg: the static GPL build from BtbN, pinned and checksummed.
FROM --platform=$BUILDPLATFORM debian:trixie-slim AS ffmpeg
ARG TARGETARCH
ARG FFMPEG_RELEASE=autobuild-2026-09-28-13-06
ARG FFMPEG_NAME=ffmpeg-n9.0.2-14-gebafaee10a
ARG FFMPEG_SHA256_AMD64=58e27dab85141ff1e08f43488b7700cc4443e17571bbee809edd6486d9dfd9b2
ARG FFMPEG_SHA256_ARM64=ef90df8e233d3ad1d18a802fc3d9e3a256f7f14cc7efad3d20cd035318c73512
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl xz-utils \
    && rm -rf /var/lib/apt/lists/*
RUN set -eu; \
    case "$TARGETARCH" in \
      amd64) arch=linux64; sum="$FFMPEG_SHA256_AMD64" ;; \
      arm64) arch=linuxarm64; sum="$FFMPEG_SHA256_ARM64" ;; \
      *) echo "unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    file="${FFMPEG_NAME}-${arch}-gpl-9.0.tar.xz"; \
    curl -fsSL -o /tmp/ffmpeg.tar.xz "https://github.com/BtbN/FFmpeg-Builds/releases/download/${FFMPEG_RELEASE}/${file}"; \
    echo "${sum}  /tmp/ffmpeg.tar.xz" | sha256sum -c -; \
    mkdir -p /opt/ffmpeg; \
    tar -xJf /tmp/ffmpeg.tar.xz -C /opt/ffmpeg --strip-components=1; \
    rm /tmp/ffmpeg.tar.xz

# Final image.
FROM debian:trixie-slim
# Hardware acceleration drivers (VAAPI/QSV) are not installed yet: they need to be validated
# on real hardware first.
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /config /cache && chown 1000:1000 /config /cache
COPY --from=ffmpeg /opt/ffmpeg/bin/ffmpeg /opt/ffmpeg/bin/ffprobe /usr/local/bin/
COPY --from=build /out/laterna /usr/local/bin/laterna

ENV LATERNA_DATA_DIR=/config/data \
    LATERNA_METADATA_DIR=/config/metadata \
    LATERNA_CACHE_DIR=/cache \
    LATERNA_LOG_FORMAT=json
VOLUME ["/config", "/cache"]
EXPOSE 8096
USER 1000:1000
HEALTHCHECK --interval=30s --timeout=6s --start-period=10s CMD ["laterna", "healthcheck"]
ENTRYPOINT ["laterna"]
CMD ["serve"]

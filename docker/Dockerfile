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

# Stock FFmpeg: the static GPL build from BtbN, pinned and checksummed in packaging/ffmpeg.lock.
FROM --platform=$BUILDPLATFORM debian:trixie-slim AS ffmpeg
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl xz-utils && rm -rf /var/lib/apt/lists/*
COPY packaging/ffmpeg.lock packaging/fetch-ffmpeg.sh /packaging/
RUN sh /packaging/fetch-ffmpeg.sh linux "$TARGETARCH" /opt/ffmpeg/bin

# Web client: a release of laterna-web, pinned and checksummed in packaging/web.lock. Static files,
# the same for every architecture.
FROM --platform=$BUILDPLATFORM debian:trixie-slim AS web
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl && rm -rf /var/lib/apt/lists/*
COPY packaging/web.lock packaging/fetch-web.sh /packaging/
RUN sh /packaging/fetch-web.sh /opt/laterna-web

# Final image. CI never takes this stage from the build cache, so every image that is pushed has
# the security updates of the day it was built.
FROM debian:trixie-slim AS runtime
# Hardware acceleration drivers (VAAPI/QSV) are not installed yet: they need to be validated
# on real hardware first.
RUN apt-get update && apt-get upgrade -y \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /config /cache && chown 1000:1000 /config /cache
COPY --from=ffmpeg /opt/ffmpeg/bin/ffmpeg /opt/ffmpeg/bin/ffprobe /usr/local/bin/
COPY --from=build /out/laterna /usr/local/bin/laterna
COPY --from=web /opt/laterna-web /usr/share/laterna/web

ENV LATERNA_DATA_DIR=/config/data \
    LATERNA_METADATA_DIR=/config/metadata \
    LATERNA_CACHE_DIR=/cache \
    LATERNA_WEB_DIR=/usr/share/laterna/web \
    LATERNA_LOG_FORMAT=json
VOLUME ["/config", "/cache"]
EXPOSE 8096
USER 1000:1000
HEALTHCHECK --interval=30s --timeout=6s --start-period=10s CMD ["laterna", "healthcheck"]
ENTRYPOINT ["laterna"]
CMD ["serve"]

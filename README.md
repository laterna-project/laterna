# Laterna

Laterna is a self-hosted media server for movies, series, music, books and photos. It is written
from scratch in Go, ships as a single static binary, and stores everything in one SQLite file.

This repository is the **server**. It exposes its own API, defined as a Protobuf contract and
served with [ConnectRPC](https://connectrpc.com/). Laterna is **not compatible with the Jellyfin,
Emby or Plex APIs**, so their apps do not work with it: clients are written against the contract
in [`proto/`](proto/laterna/v1). The web client, [Laterna Web](https://github.com/laterna-project/laterna-web),
is one of them, and the Docker image serves it. A small development console is included to try the
server without a client.

> The project is young. The server covers the scope below and is well tested, but it has only
> run on a handful of machines. Until version 1.0, a minor version may change the configuration
> or what the API offers; the release notes say how to upgrade.

## What it does

- **Playback that starts fast.** Direct play when the device can play the file; otherwise HLS
  with segments aligned on the source keyframes, without re-encoding what the device can already
  play. Each stream is copied or transcoded on its own. Hardware encoders, HDR to SDR tone mapping
  and a full on-GPU chain are detected by real attempts at startup, with fallbacks down to
  `libx264`.
- **Instant subtitles.** Tracks, external files and fonts are extracted at import and served
  separately, so switching subtitles restarts nothing. Burn-in is a last resort. A missing
  subtitle can be asked for: Bazarr finds it.
- **Metadata from your files.** NFO files and artwork next to the media, as written by Sonarr,
  Radarr, Lidarr, Kodi or tinyMediaManager. No online lookup, no wrong match. Optional Sonarr,
  Radarr and Lidarr integration with webhooks.
- **Requests.** Profiles ask for movies, series, music and books; an administrator approves, and
  Sonarr, Radarr, Lidarr or LazyLibrarian fetch them. Each request is followed until it can be
  watched, listened to or read.
- **Notifications.** A profile is told what became of its requests and when episodes of the
  series it follows arrive, in the app or by web push when it is closed. The home page shows what
  Sonarr, Radarr and Lidarr expect next.
- **Files keep their identity.** A renamed or moved file keeps its watch state, and a share that
  goes offline does not wipe a library.
- **Households.** Accounts with profiles, PINs, per-library access and parental controls
  enforced on the server. Sign-in by password, passkey, OpenID Connect, or a code shown on a TV.
- **More than video.** Music from tags with whole-file playback, EPUB/CBZ/PDF books with pages
  scaled for the screen, photo libraries with EXIF and a timeline.
- **The extras.** Intro and credits detection, scrubbing thumbnails, offline downloads, watch
  parties, play history with yearly statistics, recommendations computed locally, collections
  and playlists, themes, import of users and watch state from Jellyfin.
- **Easy to run.** No external database. Hot backups, including one before every schema
  migration. Prometheus metrics and OpenTelemetry traces. Starts in well under a second and
  idles under 60 MB.

## Quick start

### Docker

```sh
docker run -d --name laterna -p 8096:8096 \
  -v laterna-config:/config -v laterna-cache:/cache \
  -v /path/to/media:/media:ro \
  ghcr.io/laterna-project/laterna:latest
```

The image includes a pinned FFmpeg build and, from version 0.2.0, the web client: open
http://localhost:8096 to set the server up. Media folders can be mounted read-only: Laterna never
writes into them. To let TVs and apps find the server on the local network, run the container
with `--network host` (multicast does not cross Docker's bridge network). `latest` is the last
release; `edge` follows the development branch.
[`deploy/compose`](deploy/compose/README.md) has ready Compose setups to combine, HTTPS included.

### Packages and archives

Releases come as `.deb` and `.rpm` packages with a systemd service, and as archives for Linux,
Windows, macOS and FreeBSD, with or without FFmpeg included. See [docs/install.md](docs/install.md).

### From source

You need [Go 1.27](https://go.dev/dl/), [go-task](https://taskfile.dev/) and FFmpeg 7.1 or later
on the `PATH`.

```sh
task build          # binary in bin/
bin/laterna         # serves on http://localhost:8096
```

### First steps

Check that the server answers, then create the first account, which is the administrator:

```sh
curl -X POST -H "Content-Type: application/json" -d '{}' \
  http://localhost:8096/laterna.v1.ServerService/GetServerInfo

curl -X POST -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"choose-a-long-one","device":{"name":"Terminal","client":"curl"}}' \
  http://localhost:8096/laterna.v1.AuthService/Setup
```

`Setup` returns a session token. With a client, the rest (libraries, accounts, settings) is done
from its administration screens; the web client does all of it, setup included. Without one, every call works the same way with
`-H "Authorization: Bearer <token>"`; the services are listed in
[`proto/laterna/v1`](proto/laterna/v1).

To try playback without a client, run the development console and open http://127.0.0.1:8097:

```sh
go run ./devtools/console
```

## Configuration

Startup configuration comes from an optional TOML file (`laterna.toml` in the working directory,
`-config <file>` or `LATERNA_CONFIG`), overridden by environment variables. Everything else
(server name, scan interval, public URL, transcode limit…) is a setting changed at runtime through
the API.

| Variable | TOML key | Default | Purpose |
|---|---|---|---|
| `LATERNA_ADDRESS` | `server.address` | `:8096` | Listen address |
| `LATERNA_CORS_ORIGINS` | `server.cors_origins` | `*` | Allowed web origins, comma-separated |
| `LATERNA_TRUSTED_PROXIES` | `server.trusted_proxies` | none | Reverse proxies (addresses or CIDR ranges) whose `X-Forwarded-For` is trusted |
| `LATERNA_DISCOVERY` | `server.discovery` | `true` | Announce the server on the local network (mDNS, `_laterna._tcp`) |
| `LATERNA_DATA_DIR` | `paths.data` | per OS | Database and logs: the part to back up |
| `LATERNA_METADATA_DIR` | `paths.metadata` | per OS | Downloaded images, extracted subtitles, thumbnails |
| `LATERNA_CACHE_DIR` | `paths.cache` | per OS | Safe to delete |
| `LATERNA_BACKUP_DIR` | `paths.backups` | `<data>/backups` | Database backups; put it on another disk |
| `LATERNA_WEB_DIR` | `paths.web` | none (the image: its web client) | An unpacked [web client release](https://github.com/laterna-project/laterna-web/releases), served next to the API |
| `LATERNA_LOG_LEVEL` | `log.level` | `info` | `debug`, `info`, `warn`, `error` |
| `LATERNA_LOG_FORMAT` | `log.format` | `text` | `text` or `json` on standard output |
| `LATERNA_FFMPEG`, `LATERNA_FFPROBE` | `ffmpeg.ffmpeg`, `ffmpeg.ffprobe` | from `PATH` | FFmpeg executables |
| `LATERNA_FFMPEG_ENCODER` | `ffmpeg.encoder` | best that works | Force the H.264 encoder (`libx264`, `h264_nvenc`, `h264_qsv`, `h264_amf`, `h264_vaapi`, `h264_videotoolbox`, `h264_v4l2m2m`, `h264_mf`) |

```toml
[server]
address = ":8096"
trusted_proxies = ["127.0.0.1", "172.16.0.0/12"]

[paths]
backups = "/mnt/other-disk/laterna-backups"
```

Behind a reverse proxy, declare it in `trusted_proxies`; otherwise every client appears to come
from the proxy and shares the same rate limits.

**Backups.** The database is copied every night (7 kept by default) and before each schema
migration. `laterna backup` takes one right now, and `laterna restore <backup>` stages a restore
that is applied at the next start.

**Metrics and traces.** `/metrics` (Prometheus text format) stays closed until an administrator
opens it and receives a dedicated token. Traces are exported over OTLP/HTTP when the standard
`OTEL_EXPORTER_OTLP_ENDPOINT` variable is set.

**Languages.** Errors carry a stable code and parameters, and every text the server composes
carries its key, so clients translate into any language. The server also writes a fallback text,
in English or French.

## Documentation

- [`docs/install.md`](docs/install.md): Docker, packages, archives, the web client, reverse proxy,
  upgrades.
- [`deploy/compose/`](deploy/compose/README.md): Docker Compose setups to combine (NAS media, GPU,
  HTTPS through Caddy, Traefik, nginx and others, Tailscale).
- [Laterna Web](https://github.com/laterna-project/laterna-web): the web client.
- [`docs/design/`](docs/design/README.md): how the server is built and why.
- [`proto/laterna/v1/`](proto/laterna/v1): the API contract, with comments on every message.
- [`CONTRIBUTING.md`](CONTRIBUTING.md): development setup and the rules of the code base.
- [`docs/releasing.md`](docs/releasing.md): branches, versions and how a release is cut.

## Development

```sh
task dev        # run locally on :8096, data in .dev/, debug logs
task check      # everything CI runs: generated code, lint, contract, nilaway, tests, vulnerabilities
task fixtures   # synthetic test media in testdata/library (needs FFmpeg)
task perf       # performance budgets on a synthetic catalog
```

## License

Laterna is free software: you can redistribute it and modify it under the terms of the
[GNU General Public License](LICENSE) as published by the Free Software Foundation, either
version 3 of the License or, at your option, any later version. It comes with no warranty.

# Installing Laterna

Laterna is one binary plus FFmpeg. Pick the way that fits the machine:

| | Includes FFmpeg | Runs as a service | Updates |
|---|---|---|---|
| [Docker image](#docker) | yes | the container | pull the new tag |
| [deb or rpm package](#debian-ubuntu-fedora-and-other-systemd-distributions) | yes | systemd | install the new package |
| [Archive with FFmpeg](#archives) (`_ffmpeg`) | yes | up to you | replace the folder |
| [Archive without FFmpeg](#archives) | no, bring FFmpeg 7.1 or later | up to you | replace the binary |

Releases are on the [releases page](https://github.com/laterna-project/laterna/releases). Once the
server runs, continue with the [first steps](../README.md#first-steps).

## Platforms

| Platform | Release files | Tested in CI |
|---|---|---|
| Linux amd64, arm64 | Docker image, deb, rpm, archives with and without FFmpeg | yes |
| Windows amd64 | archives with and without FFmpeg | yes |
| Windows arm64 | archives with and without FFmpeg | built only |
| macOS, Apple Silicon and Intel | archive without FFmpeg | Apple Silicon, without HDR conversion and subtitle burn-in |
| Linux armv7, FreeBSD amd64 | archive without FFmpeg | built only |

The FFmpeg shipped with Laterna is a pinned static GPL build from
[BtbN](https://github.com/BtbN/FFmpeg-Builds), the one the tests run against. Where no such build
exists (macOS, FreeBSD, armv7), install FFmpeg 7.1 or later yourself. It must include `libx264`;
without `libass` the server cannot burn subtitles in for devices that need it, and without
`zscale` (zimg) it cannot convert HDR sources to SDR.

## Docker

```sh
docker run -d --name laterna -p 8096:8096 \
  -v laterna-config:/config -v laterna-cache:/cache \
  -v /path/to/media:/media:ro \
  ghcr.io/laterna-project/laterna:latest
```

Or with Compose:

```yaml
services:
  laterna:
    image: ghcr.io/laterna-project/laterna:latest
    restart: unless-stopped
    ports:
      - "8096:8096"
    volumes:
      - laterna-config:/config
      - laterna-cache:/cache
      - /path/to/media:/media:ro

volumes:
  laterna-config:
  laterna-cache:
```

- Tags: `latest` and `X.Y.Z` are releases, `X.Y` follows the patches of a minor version, `edge`
  follows the `develop` branch. Until the first release is out, only `edge` exists.
- `/config` holds the database, the logs, downloaded images and backups: this is the volume to
  keep. `/cache` can be thrown away.
- The server runs as user 1000. Media can be mounted read-only; Laterna never writes there.
- To let TVs and apps find the server on the local network, use `network_mode: host` (or
  `--network host`). Multicast does not cross Docker's bridge network.
- The image ships no VAAPI or QSV drivers yet: Intel and AMD GPUs are not used, and the server
  falls back to `libx264`.

## Debian, Ubuntu, Fedora and other systemd distributions

Download the package for your architecture from the releases page, then:

```sh
sudo apt install ./laterna_X.Y.Z_amd64.deb        # Debian, Ubuntu
sudo dnf install ./laterna-X.Y.Z-1.x86_64.rpm     # Fedora, RHEL and derivatives
```

The package installs:

| Path | What |
|---|---|
| `/usr/bin/laterna` | the server |
| `/usr/lib/laterna/ffmpeg`, `ffprobe` | the pinned FFmpeg build |
| `/etc/laterna/laterna.toml` | startup configuration, kept across upgrades |
| `/var/lib/laterna` | database, logs, metadata, backups |
| `/var/cache/laterna` | cache, safe to delete |
| `laterna.service` | the systemd unit, enabled and started on install |

It creates a `laterna` system user. The server listens on port 8096.

```sh
systemctl status laterna
journalctl -u laterna -f
sudo systemctl restart laterna      # after editing /etc/laterna/laterna.toml
```

**Access to your media.** The service is sandboxed: the system is read-only for it, and it can
only read what the `laterna` user can read. Add the user to the group that owns your media, for
example `sudo usermod -aG media laterna`, then restart the service.

**Backups on another disk, GPU access.** Both need a drop-in, since the sandbox does not allow
them by default:

```sh
sudo systemctl edit laterna
```

```ini
[Service]
ReadWritePaths=/mnt/backups/laterna
SupplementaryGroups=render video
```

Upgrading is installing the newer package: the service restarts on the new version and the
database is backed up before any schema change. Removing the package leaves `/var/lib/laterna`
in place.

## Archives

Each archive unpacks into one folder. The `_ffmpeg` ones contain `ffmpeg` and `ffprobe` next to
the server, which uses them without any configuration. With the other archives, FFmpeg must be in
`PATH`, or named with `LATERNA_FFMPEG` and `LATERNA_FFPROBE`.

### Linux

```sh
tar -xJf laterna_X.Y.Z_linux_amd64_ffmpeg.tar.xz
cd laterna_X.Y.Z_linux_amd64_ffmpeg
./laterna
```

Data goes to `~/.local/share/laterna` and `~/.cache/laterna` unless configured otherwise (see the
[configuration table](../README.md#configuration)). The archive includes `laterna.service` and
`laterna.example.toml`, the files the packages install, to adapt if you want a service.

### Windows

Unzip `laterna_X.Y.Z_windows_amd64_ffmpeg.zip` and run `laterna.exe`. Data goes to
`%LOCALAPPDATA%\Laterna`.

- The binaries are not signed. SmartScreen asks for confirmation the first time, and a machine
  with Smart App Control turned on refuses to run them: use Docker there, or build from source.
- Laterna does not install itself as a Windows service yet. To start it with the machine, create a
  task in the Task Scheduler that runs `laterna.exe` at startup.

### macOS

```sh
brew install ffmpeg
tar -xzf laterna_X.Y.Z_darwin_arm64.tar.gz
cd laterna_X.Y.Z_darwin_arm64
xattr -d com.apple.quarantine laterna    # the binary is not notarized
./laterna
```

Homebrew's `ffmpeg` formula is built without `libass` and `zimg`: with it, subtitles cannot be
burned in and HDR sources cannot be converted to SDR. The `ffmpeg-full` formula has both. It is
not linked into `PATH`, so name it with
`LATERNA_FFMPEG="$(brew --prefix ffmpeg-full)/bin/ffmpeg"` and the same for `LATERNA_FFPROBE`.
CI only runs the tests against the plain formula.

Data goes to `~/Library/Application Support/Laterna` and `~/Library/Caches/Laterna`. One limit on
macOS: if the server is killed abruptly, FFmpeg processes it started can outlive it.

## Checking a download

`checksums.txt` lists the SHA-256 of every file of a release:

```sh
sha256sum --check --ignore-missing checksums.txt
```

Release files also carry a build provenance attestation, which proves they were built by this
repository's release workflow:

```sh
gh attestation verify laterna_X.Y.Z_linux_amd64.tar.gz --repo laterna-project/laterna
```

## From source

You need [Go 1.27](https://go.dev/dl/), [go-task](https://taskfile.dev/) and FFmpeg 7.1 or later.

```sh
git clone https://github.com/laterna-project/laterna.git
cd laterna
task build        # bin/laterna
```

## Behind a reverse proxy

Laterna speaks plain HTTP; terminate TLS in a reverse proxy (Caddy, Traefik, nginx) and declare
it, so the server sees real client addresses:

```toml
[server]
trusted_proxies = ["127.0.0.1"]
```

Streams are long-lived: turn off response buffering and do not set a short read timeout on the
proxy. HTTP/2 to the server is optional; the Connect protocol works over HTTP/1.1.

## Upgrading and going back

A new version migrates the database when it starts, after saving a copy of it
(`laterna-migration-v<N>-…` in the backups folder). An older version cannot open a newer database.
To go back, reinstall the older version and restore that copy with `laterna restore <backup>`; see
[Storage](design/storage.md#backups).

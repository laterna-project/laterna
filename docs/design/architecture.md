# Architecture

Laterna is a self-hosted media server. It runs for months without a restart, often on a NAS or a
small ARM box, and spends most of its time serving bytes and driving FFmpeg processes. The heavy
computation (transcoding) is done by FFmpeg, not by the server.

## Go, without cgo

The server is written in Go and built with `CGO_ENABLED=0`.

- One static binary per platform and trivial cross-compilation (amd64, arm64, Windows).
- No C compiler is needed to work on the project.
- A small memory footprint, and a goroutine plus a `context` per FFmpeg process: cancelling a
  playback kills its process.
- The SQLite driver is pure Go (`modernc.org/sqlite`).

Two weaknesses of the language are covered by tooling: sealed interfaces are checked for
exhaustive switches (`gochecksumtype`, `//sumtype:decl`), and possible nil dereferences are
caught by `nilaway`. The only place cgo appears is `go test -race`, a test tool, never the
shipped binary.

Dependencies are added only with a written reason. Several things that usually come from a
library are written in the tree because the part we need is small and the library is not: WebAuthn
verification, the OpenID Connect client, Prometheus text output, OpenTelemetry export, EXIF and
PDF reading, the Matroska reader. Each note says why.

## Layers

| Package | Role | May import |
|---|---|---|
| `internal/domain` | Business types, no I/O | nothing from the project |
| `internal/naming`, `playback`, `party`, `recommend`, `stats`, `segments`, `i18n` | Pure logic | `domain` |
| `internal/store`, `media/*`, `metadata`, `library`, `arr`, `oidc`, `jellyfin`, `discovery` | Adapters: database, FFmpeg, files, other servers | `domain`, never `app` or `api` |
| `internal/app` | Use cases and orchestration | everything except `api` |
| `internal/api/*` | Connect services (`rpc`), plain HTTP routes, middleware (`httpx`) | `app` and `domain`, never `store` or `media` |
| `cmd/laterna` | Wiring | everything |

The rules live in `.golangci.yml` (depguard): breaking one fails `task lint` and CI. The API layer
only translates between messages and domain types, so business tests never go through HTTP.
Generated code (`internal/api/gen`, `internal/store/sqlc`) is committed and never edited by hand.

## Identifiers

Every entity has a UUIDv7 (`domain.NewID`): time-ordered, so indexes stay compact and sorting by
ID sorts by creation. The exchange format is the canonical 36-character form; the 32-character
hex form is accepted on input. An ID is assigned the first time something is seen and never
depends on a file path (see [Libraries](library.md)).

## Configuration and settings

There are two levels, with no overlap.

1. **Startup configuration** (`internal/config`): what must be known before the database opens,
   or what depends on the machine. Listen address, directories, logs, FFmpeg paths, CORS origins,
   trusted proxies, local discovery. Defaults, then an optional TOML file (unknown keys are
   rejected like typos), then `LATERNA_*` variables, then command-line flags.
2. **Runtime settings** (`domain.Settings`): stored in the database, changed through the
   administration API and applied at once. See [Operations](operations.md).

Directories are kept apart: `data` (database and logs, the part to back up), `metadata`
(downloaded images, extracted subtitles, scrubbing thumbnails), `cache` (safe to delete) and
`backups`. Laterna never writes into media folders.

Defaults are `%LOCALAPPDATA%\Laterna` on Windows, the XDG directories on Linux, `~/Library` on
macOS, and `/config` plus `/cache` in the Docker image.

## Background jobs

Scanning, probing, metadata, images, subtitle extraction, thumbnails: every heavy task must
survive a restart, be deduplicated and stay out of the way of playback. The queue is a table in
the same SQLite database (`internal/jobs`), written for exactly these needs.

- Jobs are idempotent and deduplicated by `(kind, target)` among pending jobs: asking again only
  raises the priority. A job may wait while another one on the same target runs, because the
  target may have changed in the meantime.
- Each kind belongs to a **class** with its own worker count, so one kind of work cannot starve
  another:

  | Class | Workers | Used for |
  |---|---|---|
  | `scan` | 1 | Walking library folders |
  | `io` | 2 | ffprobe, NFO files, backups, purges (gentle on network shares) |
  | `cpu` | half the cores | Image analysis |
  | `net` | 2 | Image downloads |
  | `index` | 1 | Keyframe indexes |
  | `extract` | 1 | Subtitle extraction |
  | `trickplay` | 1 | Scrubbing thumbnails |
  | `segments` | 1 | Intro and credits detection |
  | `prepare` | 1 | Offline downloads of videos |
  | `convert` | 2 | Offline downloads of music |
  | `arr` | 1 | Sonarr and Radarr refreshes |

- Priorities: a user action comes before background work, and "idle" work comes last.
- A job is enqueued inside the caller's write transaction, so it exists exactly when the change
  that needs it does.
- Three attempts by default with a growing delay (30 s, doubled each time, capped at one hour);
  `Permanent(err)` skips the retries; a panic becomes a permanent failure. Each kind has a
  timeout.
- A finished job is deleted. A job that failed for good stays visible with its last error, and an
  administrator can retry or dismiss it.
- At startup, jobs left "running" by a crash go back to pending. On a clean stop, the interrupted
  job restarts without a penalty.

## Testing and quality

`task check` must pass before a change is done, and CI runs the same thing:

- generated code matches its sources and is committed;
- `golangci-lint` (architecture rules included), `buf lint`, `.proto` formatting, `sqlc vet`;
- `buf breaking` against `main`;
- `nilaway`;
- `go test -race`;
- `govulncheck`.

Tests sit next to the code and come in layers:

1. **Pure logic**: table tests and Go's native fuzzing (file names, playback decisions, CBOR,
   DNS questions).
2. **Database**: a real temporary SQLite database with all migrations, including a path with a
   non-ASCII character, and `EXPLAIN QUERY PLAN` assertions on the key list queries. No mocks.
3. **Media**: real FFmpeg on synthetic fixtures (`task fixtures`). A test that needs them calls
   `testfixtures.Library(t)` first, which generates them or skips the test when FFmpeg is absent.
4. **API**: the generated Connect client against a real HTTP server.
5. **Endurance**: random seeks and abrupt stops, with no FFmpeg process left behind.

Tests never use the network: Sonarr, Radarr, an OpenID Connect provider and an OTLP collector are
replaced by small fake servers in the tree (`arrtest`, `oidctest`, `telemetrytest`).

## Performance budgets

Three budgets are measured, not assumed:

| | Budget |
|---|---|
| Startup, until `/health` answers | under 1 s |
| Memory at rest (resident) | under 60 MB |
| List calls, 95th percentile | under 30 ms |

`task perf` (`devtools/perfcheck`) writes a synthetic catalog of 10,430 items straight into a
database (3,000 movies, 150 series of 4 seasons of 10 episodes, 20 artists of 3 albums of
10 tracks, with genres, cast, images and watch data), starts the real binary and measures startup,
resident memory after a rest, twelve API calls two hundred times each, then memory again.

On a desktop machine with the reduced page cache described in [Storage](storage.md): startup in
about 0.13 s, the worst p95 around 16 to 22 ms (the home screen), and 37 to 45 MB at rest
depending on the system.

Rules that came out of the measurements:

- Run `task perf` before touching a list query, the home screen, startup or database
  connections. A missed budget is a defect.
- After each argon2id computation (19 MiB in one block), memory is handed back to the system
  right away (`debug.FreeOSMemory`); otherwise Go keeps it for minutes.
- The image decoder, argon2id and transcodes are all bounded in how many run at once.

# Contributing

Thanks for looking at Laterna. This page covers how to build and test the server and the rules
the code base holds itself to. The reasons behind the design are in
[`docs/design/`](docs/design/README.md); reading the note for the area you touch saves time.

## Setup

You need:

- [Go 1.27](https://go.dev/dl/);
- [go-task](https://taskfile.dev/);
- FFmpeg 7.1 or later (`ffmpeg` and `ffprobe` on the `PATH`), a GPL build so that `libx264` and
  `zscale` are present;
- a C compiler, only for `task test:race` (the race detector needs cgo; the server itself is
  built without it).

Everything else is pinned in `tools/go.mod` and run through `go tool`: sqlc, buf, the protoc
plugins, golangci-lint, nilaway, govulncheck. There is nothing to install.

On Windows: `winget install GoLang.Go Task.Task Gyan.FFmpeg`.

## Commands

| Command | What it does |
|---|---|
| `task dev` | Run the server on `:8096` with data in `.dev/` and debug logs. Arguments after `--` go to the server (`task dev -- -address :9000`). |
| `task check` | Everything that must pass before a change is done; CI runs the same. |
| `task gen` | Regenerate code: sqlc from `migrations/` and `internal/store/queries/`, buf from `proto/`. |
| `task test`, `task lint`, `task fmt`, `task build` | The usual. |
| `task fixtures` | Generate synthetic test media in `testdata/library` (about 20 MB, not committed). |
| `task perf` | Measure the performance budgets on a synthetic catalog. Run it before touching a list query, the home screen or startup. |
| `task docker:build` | Build the local image `laterna:dev`. |
| `sh docker/smoke-test.sh laterna:dev` | Start that image and check the server and FFmpeg answer. |
| `sh deploy/compose/test/test.sh` | Check the Compose modules of `deploy/compose` with the published image; `LATERNA_IMAGE=laterna LATERNA_VERSION=dev` for that local one. |

Development tools:

- `go run ./devtools/console`: a playback console in the browser (http://127.0.0.1:8097). It
  builds its device profile from the browser, and codecs can be unticked to force a transcode.
- `go run ./devtools/namingcheck -movies <dir> -shows <dir>`: run a real library through the
  file-name parser and report doubtful cases, without writing anything.

## Rules of the code base

**The contract comes first.** A new API feature starts in `proto/`, then `task gen`. Every
method declares its access level. `buf breaking` rejects changes that would break existing
clients. Generated code is committed and never edited by hand.

**Layers are enforced.** `internal/domain` does no I/O and imports nothing from the project.
Pure-logic packages (`naming`, `playback`, `party`, `recommend`, `stats`, `segments`, `i18n`)
only import `domain`. Adapters (`store`, `media`, `metadata`…) never import `app` or `api`. The
API layer goes through `app`, never straight to the database, and holds no business logic.
depguard checks all of this; a violation fails the lint.

**No user-facing sentence in the code.** An expected error is written
`domain.NotFound("area.reason", "name", value…)`, any other text `domain.T("family.key", …)`.
The message goes in `internal/i18n/locales/en.json` **and** `fr.json`, without plurals; durations
as `…_seconds`, sizes as `…_bytes`. A test fails if a key is missing or unused. Logs, internal
errors and the command line are in English.

**Errors.** Expected cases (not found, invalid argument) are domain errors. Anything else is
logged with the request ID and reaches the client as `server.internal`.

**Database.** Writes go through `store.Write` (one writer); reads use the read pool and never
hold two connections at once. Migrations are goose SQL files in `migrations/`: IDs are 16-byte
`BLOB`s and nothing else is a `BLOB`, dates are Unix milliseconds, booleans are 0/1 with a
`CHECK`, tables are `STRICT`.

**FFmpeg.** Always started through `proc.Command`, with arguments as an array and inputs prefixed
with `file:`. Strings that come from disk or from ffprobe go through `strings.ToValidUTF8` before
they are exposed.

**Tests.** Tests live next to the code and use a real temporary SQLite database, not mocks. The
API is tested with the generated Connect client against a real server. A test that needs the
fixtures calls `testfixtures.Library(t)` first: it generates them, or skips the test when FFmpeg
is missing. Tests never use the network; fake servers stand in for Sonarr, Radarr, OpenID Connect
providers and OTLP collectors. Do not make a test depend on the machine it runs on (core count,
GPU): pin what it reads.

**Dependencies.** None is added without a written reason, and there is one way of doing each
thing. Several small readers and clients are written in the tree for that reason; see the design
notes.

**Style.** Comments explain why, in plain English. Doc comments start with the name of what they
document. Match the code around you.

## Branches

The project follows git-flow. For a contribution, only two branches matter:

- **`develop`** is the default branch and the one to start from. It holds the next release.
- **`main`** only holds released versions. Do not open pull requests against it.

Name your branch after what it does: `feature/…`, `fix/…`, `docs/…`, `ci/…`. Maintainers handle
`release/*` and `hotfix/*`; the whole model, versions and the release steps are in
[docs/releasing.md](docs/releasing.md).

## Submitting a change

1. Open an issue first for anything that changes the contract or the design.
2. Branch from `develop`, keep the change focused, and update the design note it contradicts or
   extends.
3. Run `task check`.
4. Open a pull request against `develop`. It is squashed when merged, and its title becomes the
   commit subject: write it in English, in the imperative, with a
   [gitmoji](https://gitmoji.dev/) in front (`✨ Add …`, `🐛 Fix …`, `♻️ Refactor …`). Release notes
   are built from these subjects.

CI runs the checks on Linux for every pull request, cross-compiles every release target, runs the
tests on Windows, macOS and Linux arm64, and builds and starts the Docker image.

Everyone taking part is expected to follow the [code of conduct](CODE_OF_CONDUCT.md).

By contributing you agree that your work is released under the project's license, the GNU General
Public License, version 3 or any later version.

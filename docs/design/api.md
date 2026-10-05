# API

## A contract first, and no Jellyfin compatibility

Laterna's API is defined in Protobuf (`proto/laterna/v1`) before any code is written, and served
with [ConnectRPC](https://connectrpc.com/). The same handlers speak the Connect protocol (JSON or
binary over HTTP/1.1, usable from a browser with `fetch`), gRPC and gRPC-Web. Cleartext HTTP/2 is
enabled for native gRPC clients.

The project started as a server compatible with the Jellyfin API and dropped that goal early.
Compatibility meant inheriting the shape of that API: one item type with about 150 optional
fields, a list endpoint with dozens of parameters, durations in 100 ns ticks, a custom
authorization header. Laterna will not be compatible with it, now or later. The price is that no
existing app works with Laterna: clients have to be written. Their API layer is generated from
the contract (TypeScript, Kotlin, Swift). Importing *data* from Jellyfin is a different matter and is
supported (see [Importing from Jellyfin](jellyfin-import.md)).

Rules of the contract:

- One service per domain (`CatalogService`, `PlaybackService`, `MusicService`…) and one message
  per kind of item, with fields that are always filled in.
- Reads without side effects are marked `NO_SIDE_EFFECTS`, so they can be called with GET and
  cached.
- `google.protobuf.Duration` and `Timestamp` for time, cursors for paging.
- `buf lint` (STANDARD rules) and `buf breaking` run in CI: a change that would break an
  existing client is rejected.
- Every new feature starts with the `.proto` file, then `task gen`.

A test checks that every method of the contract is actually served.

## Access levels are part of the contract

Each method declares who may call it with `option (access)` (`options.proto`):

| Level | Caller |
|---|---|
| `ACCESS_PUBLIC` | Anyone |
| `ACCESS_ACCOUNT` | A signed-in account, profile picked or not |
| `ACCESS_PROFILE` | A signed-in account with a picked profile |
| `ACCESS_ADMIN` | An administrator account, not on a restricted profile |

A method without the option is `PROFILE`, the most common and a strict one, so a method cannot
stay open by oversight. One interceptor reads the option, authenticates the bearer token and puts
the caller in the context. Handlers never check access themselves.

Received messages are limited to 4 MiB once decompressed (32 MiB for the theme service, which
receives images).

## Errors

Expected failures are domain errors (`domain.NotFound`, `Invalid`, `Conflict`, `Forbidden`,
`Precondition`, `Busy`…). The API layer maps them to Connect codes and attaches a
`laterna.v1.ErrorDetail` with a stable code such as `library.not_found`, named parameters and
sometimes causes. The message is a fallback text in the language of the request. See
[Internationalization](i18n.md).

Anything else is logged with the request ID and returned as `server.internal`, with no detail.

## Catalog

The catalog is the most called part of the API: poster grids, detail pages, search.

- **Summaries and details.** `MovieSummary` and `SeriesSummary` for lists; `Movie`, `Series`,
  `Season`, `Episode` for detail pages. Files (`MediaFile`: streams, chapters, availability) only
  come with the detail of a movie or an episode.
- **Dedicated list calls** (`ListMovies`, `ListSeries`, `ListEpisodes`…) with explicit filters
  (genre, played, favorite) and four sort orders (title, added, released, rating), each
  reversible.
- **Cursor paging.** The page token is opaque and carries the sort order, the sort value and the
  ID of the last item (`(value, id) > (?, ?)`). The cost is the same for any page, and the order
  is stable between equal values. A token presented to a list with another order is rejected.
  `total_size` gives the count across all pages.
- **Presence.** An item is listed only if at least one of its files is present. While a vanished
  file is in its grace period, its detail page still opens and shows the file as unavailable.
- **Watch data per profile**: played, play count, position, last played, favorite. For a series
  or a season, "played" is derived from its present episodes; marking it played marks them all.
  The same goes for an artist or an album and its tracks.
- **Search** uses SQLite FTS5 on titles and original titles, with the `unicode61
  remove_diacritics 2` tokenizer and a prefix index: "amelie" finds "Amélie", "chi" finds
  "Chihiro". The input is split into words, each searched as a quoted prefix, so FTS5 syntax in
  the query is never interpreted. Movies and series come before episodes, then by relevance.
- **Visibility.** Every catalog read takes a `domain.Viewer` (profile, allowed libraries,
  parental control) and only returns what that viewer may see. A forbidden item is *not found*,
  never "forbidden": the answer does not reveal that it exists. See [Accounts](accounts.md).

## Bytes over plain HTTP

Players and `<img>` tags do not speak RPC and often cannot send headers, so bytes are served by
ordinary routes.

| Route | Auth | Notes |
|---|---|---|
| `GET /images/{id}/{hash}?w=` | none | Immutable; see below |
| `GET /playback/{session}/{secret}/…` | the secret in the path | Playlist, init segment, segments, direct file, subtitles |
| `GET /fonts/{hash}{ext}` | none | Fonts of ASS subtitles, immutable |
| `GET /trickplay/{file}/{key}/{n}.jpg` | none | Scrubbing thumbnail sheets, immutable |
| `GET /books/{file}/{key}/file`, `…/pages/{n}?w=` | the key in the path | Book file and pages |
| `GET /downloads/{id}/file`, `…/subtitles/{i}.{fmt}` | bearer token of the device | Range requests |
| `GET /logs/{name}`, `GET /backups/{name}` | bearer token of an administrator | |
| `POST /hooks/{sonarr\|radarr}` | Basic auth with the webhook secret | |
| `GET`/`POST /auth/oidc/callback` | the provider's state | OpenID Connect return page |
| `GET /metrics` | dedicated bearer token | Closed until an administrator opens it |
| `GET /health` | none | |

On these routes an error carries its code in the `Laterna-Error` header and the fallback text in
the body.

### The web client

When `paths.web` names a folder (an unpacked release of the web client), the server serves it
behind every other route, so the app and the API share one origin and need no CORS or second
host. A path that names a file of the folder gets it; files under `assets/` are named after their
content and cached as immutable, the rest is revalidated on each load. Any other `GET` that a
browser makes to navigate (`Accept` includes `text/html`) gets the client's `index.html`, because
the client routes its own addresses. Everything else stays a 404: an unknown API call or a missing
script never receives a page of HTML. The folder is read-only for the server, and the router cleans
paths before they reach it, so nothing outside it is ever served.

### Images

`GET /images/{id}/{hash}[?w=width]` needs no authentication, because an `<img>` tag sends no
header. The address cannot be guessed: it holds the random part of a UUIDv7 and 64 bits of content
hash, and it is only handed out with an item the caller may see. A stale hash is a 404, so the
address changes when the image does, which allows `Cache-Control: public, max-age=31536000,
immutable`.

The width is rounded up to a tier (160 to 1920 px). Reduced versions are made on first request
(JPEG quality 85, PNG when the image has transparency), stored in the cache directory by hash,
never upscaled, with a bounded number of resizes running at once.

## Events

`EventService.Subscribe` is a server stream. Events are typed (`oneof`) and say **what changed,
not the new state**: the client reloads what it is showing.

- `LibrariesChanged` goes to everyone and `ItemsChanged` to those who may see the library;
  `LibraryScanned` to administrators; `UserDataChanged` only to the profile concerned, so its
  other devices update; `DownloadsChanged` only to the device concerned; `ThemesChanged` to
  everyone or to one profile.
- `ItemsChanged` is batched: at most one event per library every 2 s with at most 100 IDs,
  otherwise `truncated` (reload the library). A first scan of thousands of files does not flood
  clients.
- The bus is in memory (`internal/events`) and publishing never blocks. A subscriber that falls
  behind loses events and receives `Resync` (reload everything) before the rest.
- A `Heartbeat` is sent on opening and every 30 s of silence.

Events do not survive a disconnect or a restart: on reconnecting, the client reloads what it
shows. There is no replay and no sequence number, which is fine as long as events carry no state.
The profile of a stream is the one picked when it was opened; after `SelectProfile` the client
reopens the stream.

One trap worth knowing: the access-log middleware must expose `Flush`, or every Connect stream
fails behind it. A test covers this.

# Metadata

## Local files only

Laterna has no online metadata provider. The metadata of an item comes from:

1. the names of its folders and files;
2. overridden by its **NFO** file (Kodi format).

Nothing else is kept: what disappears from the NFO disappears from the item.

An earlier version looked items up at AniList and TVmaze, by ID or by title. Two problems showed
up on a real library. Matching by title is a guess, and a wrong match is worse than no match. And
every server ends up calling third-party APIs, with their rate limits, outages and licenses.

Meanwhile, most people who run a media server already manage their series with Sonarr and their
movies with Radarr. Both know *which* work is in each folder, because someone told them when
adding it, and both can write NFO files and images next to the media ("Kodi (XBMC) / Emby"
metadata). So Laterna reads those. A library without Sonarr or Radarr works the same way with
NFO files from tinyMediaManager, Kodi or another server.

Consequences:

- An item is never wrong because of a bad match. It is as rich as what the NFO says and it
  updates when the NFO is rewritten.
- Without an NFO, an item only has the title taken from its name.
- The language is the language of the NFO files. Laterna does not change that setting in Sonarr
  or Radarr.
- No data license to credit: the data is the user's own.

## What is read

`internal/metadata` parses NFO files: titles, plot, dates, rating, age rating, genres, studios,
cast with photos, crew, IDs, image addresses (`<thumb aspect>`, `<fanart>`), the collection
(`<set>`, in both forms Kodi knows), `album.nfo` and `artist.nfo`. Only http(s) addresses are
kept.

**Images** are the ones placed next to the media, read in place: `poster.jpg`, `fanart.jpg`,
`<file>-thumb.jpg`, `season01-poster.jpg`, `cover.jpg`, `artist.jpg`… regardless of case. For a
kind of image with no local file, the one whose address the NFO gives is **downloaded** into the
metadata directory. The file is named after the address, so a new address gives a new file. An
image that is unavailable (4xx, not an image, over 20 MB) is dropped without a failed job.

**Episode stills.** An episode with no thumbnail of its own, neither next to its file nor in its
NFO, gets a frame of its video. That is the case of an episode that aired the day before, when
Sonarr writes an empty `<thumb />` because the TV databases have no picture yet. The frame is
taken at 20% of the runtime, past the recap, the cold open and the opening titles of most
episodes. FFmpeg keeps the most representative of the 50 frames from there (no black frame or
blurred cut), 1280 px wide at most, converted to SDR for HDR video. It is extracted once per
content of the file, after its analysis, and kept in the metadata directory. It is only the last
resort: a thumbnail that appears later replaces it, and the purge deletes the frame. The episodes
already in a library when the server is updated have their metadata read again once, in the
background, after any other waiting work (migration 26).

**Cast photos** are downloaded from the `<thumb>` of each actor. A person keeps the **first**
photo obtained: Sonarr and Radarr give different addresses for the same person, who would
otherwise change face at every refresh.

Downloads are polite (`internal/metadata/download`): one request every 250 ms per site, a 429's
requested wait is honored, the user agent identifies Laterna. The "download images" setting
turns off every outgoing request.

Each image is analyzed once (dimensions, [BlurHash](https://blurha.sh/), content hash) and served
through the image route described in [API](api.md).

## Noticing changes

The scan records the NFO files and images of each folder (names, sizes, dates) and compares
their signature with the previous scan. For a folder that changed, the item of each file in it
is refreshed, along with the season of an episode. If it is a series folder, the series and its
seasons are refreshed too, since their posters live there. A folder under an unreachable root
keeps its signature.

A daily purge removes downloaded files that are no longer used and people with no credit left.

## Sonarr, Radarr and Lidarr integration

The integration is optional (`IntegrationService`, administrators). Laterna never copies their
database: it only reads the files they write. Lidarr (music, API v1) is handled like Sonarr and
Radarr: Kodi metadata (`artist.nfo`, `album.nfo`, artist and album images), the webhook on
imports, upgrades, renames, retags and deletions, the full refresh (`RefreshArtist`), and the
artists without an `artist.nfo`. LazyLibrarian (books) is linked too, for requests only
(docs/design/requests.md): it writes no Kodi metadata and has no webhook, so only its connection
is checked.

- **Connection**: address and API key, stored in the database and tried before being saved. The
  key never comes back out through the API.
- **Status**: identity and version, whether Kodi metadata is enabled and which options are
  missing, whether the webhook is installed, and which series or movies have no NFO. The folder
  as the instance sees it, often inside a container (`/tv/…`), is matched to a folder under a
  library root without any setting (`arr.MapPath`).
- **Configuring the instance**, only on explicit request, since each step changes its
  configuration: enable Kodi metadata with the useful options and leave the others alone; install
  the webhook; ask for a full refresh, which is followed to its end and then by a scan.
- **Webhook** `POST /hooks/{sonarr|radarr|lidarr}`: Basic authentication with a secret handed to the
  instance and stored hashed. After an import, a rename or a deletion, the libraries concerned
  are rescanned **30 s later**, which leaves time for the NFO and images to be written. Events
  close together give a single scan.

Neither Sonarr nor Radarr gives photos for the crew.

## Collections from NFO files

Radarr writes the collection of a movie in its NFO (`<set>`). Each time a movie or series is
refreshed, it is filed under that collection, identified by its TMDb collection ID when there is
one and by its normalized name otherwise. An item leaves the collections its NFO no longer names,
and an NFO collection that becomes empty disappears. See
[Music, books and photos](media-types.md#collections-and-playlists) for collections made by hand
and for playlists.

## Age ratings

The rating written in an NFO is converted to an age (`domain.RatingAge`) and stored with the
item. That age is what parental controls filter on; see [Accounts](accounts.md).

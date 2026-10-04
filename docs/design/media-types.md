# Music, books and photos

Movies and series came first. The other kinds of library reuse as much of that as they can:
items in the catalog, per-profile data, images, search, library access. This note covers what is
specific to each, then collections and playlists.

None of these kinds carries an age rating, so parental age limits do not filter them; library
access does (see [Accounts](accounts.md)).

## Music

What sets music apart: **tags are the truth** (Vorbis, ID3, MP4 say more than the file name, and
the cover is often embedded), listening has its own requirements (gapless, seeking anywhere,
ReplayGain), and formats are many.

**Model.** Artist ← album ← track are catalog items, like series ← season ← episode. The
`tracks` table holds what is specific to a track: disc, number, track artists as written,
ReplayGain (gain and peak, track and album).

**Identity**, tags first and path as a fallback (`naming.ParseTrack`):

- the **artist** is the album artist, otherwise the track artist, otherwise the folder. Failing
  everything, "Unknown Artist"; a compilation without an album artist goes under "Various
  Artists";
- the **album** is grouped by artist and normalized title;
- the **track** is grouped by album, disc and number;
- two files of the same track (FLAC and MP3) are two versions of it;
- retagging creates new items, and plays and favorites are lost. There is no tracking like for a
  renamed series folder.

**Metadata** is local only:

- a track takes everything from its tags, written as soon as it is probed;
- an album first summarizes its tracks (latest year, genres by frequency, total duration), then
  takes the MusicBrainz IDs of its first track, then its `album.nfo`;
- an artist takes the MusicBrainz ID from tags, then `artist.nfo` if the folder above the album
  bears the artist's name;
- the tags of each file are kept in the database as JSON and re-read without running ffprobe
  again.

**Images.** Album: `cover`, `folder`, `front`… in its folder (and above a disc folder such as
"CD1"), otherwise the cover embedded in one of its tracks, copied without re-encoding, otherwise
an address from an NFO. Artist: `artist`, `folder`, `poster`. A track shows its album's cover.

**Playback.**

- Direct if the device plays the container and codec.
- Otherwise **converted to AAC** in a whole MP4 (`+faststart`) kept in the cache: 128 kbit/s per
  channel up to stereo, 64 beyond. The file is named after the source fingerprint and the audio
  stream, shared by simultaneous playbacks and deleted 30 days after its last use. The `direct`
  route serves it and waits, if needed, for the conversion to finish.
- **Never HLS for music.** A whole file allows gapless playback, seeking anywhere and keeping
  the file. The same conversion serves offline downloads.
- The queue (album, shuffled artist, playlist) is kept by the client; a track is played by its
  ID.

The first playback of a converted track waits for the conversion (a couple of seconds for a
four-minute track), then nothing. Clients preload the next track, so it mostly shows on the first
one.

Not done: lyrics, "appears on" for guest artists, radios and mixes, a Lidarr integration.

## Books

A **books** library holds EPUB, PDF and CBZ files. A book is an item; the volumes of a **book
series** are ordered by number (0.5 or 12.5 are possible).

Nobody writes NFO files for books. The local sources are in the files themselves (the OPF of an
EPUB, `ComicInfo.xml` in a CBZ, the `Info` dictionary of a PDF), next to them (a Calibre library:
`metadata.opf`, `cover.jpg`) and in the names.

- **Metadata, weakest to strongest**: the name (series, volume, title, year; scan-group tags
  removed), then what the file says, then the `metadata.opf` next to it. Authors become
  `writer` credits, artists `illustrator`.
- **Cover**: `cover.jpg` (or `<file>.jpg`) next to the book, otherwise the one from the file,
  extracted into the metadata directory.

**Three ways to read**, decided when the file is analyzed:

- `IMAGES`: CBZ, and PDF in which every page is a single JPEG image (a scan). The server gives
  the list of pages with their dimensions (so a double page can be spotted) and serves each page
  **scaled to the requested width**: a phone does not download 10 MB per page.
- `REFLOWABLE`: EPUB, rendered by the client (Readium, epub.js…) from the file.
- `DOCUMENT`: any other PDF, rendered by the client.

Reading direction is right to left when the file says so (`page-progression-direction`,
`Manga = YesAndRightToLeft`).

The PDF reader is minimal and written in the tree (`internal/media/books`): classic and stream
cross-reference tables, object streams, the page tree, `Info`. It recognizes a scanned PDF and
serves its images as they are. A PDF it does not understand (damaged, encrypted) is still
readable, rendered by the client.

**Addresses**: `GET /books/{file}/{key}/file` (whole file, Range requests) and
`…/pages/{n}?w=`. The key, drawn at random at each analysis, stands in for authentication, so a
reader can cache and preload without special headers.

**Progress per profile**: a page, or an opaque location given by the reader (an EPUB CFI) with a
fraction from 0 to 1. Finished at 98% means played. Books in progress feed the "Continue reading"
row.

Scaling a scanned page takes 0.25 to 1 s (decoding a 3000 × 4600 JPEG or a 10 MB PNG), so the
server prepares the **next two pages** at the same width while the current one is read. Turning
a page then takes a few milliseconds.

Not done: CBR and CB7 (would need a RAR or 7z reader, hence a dependency), pages of a text PDF
served as images, offline downloads through `DownloadService` (the file address is enough for a
reader to keep it).

## Photos

A **photos** library holds JPEG, PNG, WebP and GIF. There is one **album** per folder, nested
like the folders; a photo at the root has no album. Thumbnail folders written by a NAS (`@eaDir`)
are ignored.

- **EXIF is read without a dependency** (`internal/media/exif`: JPEG, PNG `eXIf`, WebP `EXIF`):
  date taken, with its UTC offset when given, orientation, camera, lens, aperture, exposure, ISO,
  focal length, GPS position. Without an EXIF date, the file's date is used.
- **The photo is its item's image**, so it goes through image analysis (dimensions, BlurHash,
  content hash) and through the image route with its cached reduced versions. EXIF orientation is
  applied everywhere: announced dimensions, preview, reduced versions (scaled first, rotated
  after, which is far cheaper).
- **Thumbnails are prepared at analysis.** The image is decoded anyway for the BlurHash, so its
  240 and 480 px versions are written at the same time. The first grid of a library decodes no
  photo: 0.2 s for a grid of 60 instead of several seconds.
- A **moved photo** (same fingerprint) keeps its identity and changes album. An emptied album
  disappears, and so does its parent if that empties it too.
- **Timeline**: photos from the most recent date taken, in pages, with a count per month for a
  scroll bar. Albums show their most recent photo as a thumbnail.
- Photos are not searchable (names like "IMG_1234"); their albums are.

Not done: videos in a photo library, HEIC and AVIF, a map.

Books and photos are not played through `PlaybackService`: playlists and watch parties refuse
them.

## Collections and playlists

Two ways of organizing the catalog: **grouping** (a saga, a director's work, an administrator's
selection) and **chaining** (an evening, a marathon, a watch list in a chosen order).

**Collections** are shared by the whole server (`CollectionService`):

- **from NFO files**: `<set>` files a movie or series under its collection at every refresh
  (see [Metadata](metadata.md)). Their name and overview follow the NFO and are not edited by
  hand;
- **made by hand** by an administrator: name, overview, movies and series;
- readable by any profile, writable by administrators;
- items are **ordered by release date**: a saga reads in order. For a free order, use a playlist;
- **filtered like the rest of the catalog**: a profile only sees collections in which it can see
  an item, and only those items;
- the thumbnail is made of the posters of the first four items.

Many TMDb collections have a single movie in a given library. The server returns them with their
count and the client decides whether to show them.

**Playlists** belong to a profile (`PlaylistService`):

- movies, episodes and tracks in a chosen order; an entry has its own ID, and the same item can
  appear more than once;
- adding a season adds its present episodes in order; a series adds those of its seasons without
  specials; an album or an artist adds its tracks;
- at most 100 playlists per profile and 2,000 entries per playlist;
- what the profile can no longer see (parental control, vanished file) does not show but stays
  in the playlist, and comes back if access does.

There is no playlist shared between profiles yet.

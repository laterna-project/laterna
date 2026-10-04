# Libraries

A library is a kind (movies, series, music, books or photos) and one or more absolute folders that
exist and do not overlap with another library. Laterna reads those folders and never writes into
them.

## Scanning

A scan compares the disk to what is known. `library.Walk` lists the media files the library's
kind recognizes, plus subtitles, NFO files and images, and reports unreachable roots and
unreadable folders separately. The scan then decides what is new, changed, moved or gone, and
enqueues the work: probing, metadata, subtitles, thumbnails.

Scans run at startup, at the interval set by the administrator (6 h by default), on demand, when
Sonarr or Radarr call the webhook, and when a watched folder changes. Writes go in batches of
200 files so the write lock is released regularly.

File and folder names are parsed by `internal/naming`, pure logic with table tests and fuzzing:
movie folders with year, version and part; `S01E02`, multi-episode files, season folders,
absolute numbering as written by Sonarr ("S03E13 (054)") or by fansub groups; tracks and disc
folders; book volumes. Extras, samples, hidden files and NAS system folders are ignored.
`devtools/namingcheck` runs a real library through the parser and reports doubtful cases without
writing anything.

## File identity: a fingerprint, not a path

In many media servers, renaming or moving a file creates a new item: the "played" state,
favorites and resume position are lost. And when a network share drops, a whole library can
vanish. Laterna avoids both.

- The **fingerprint** of a file is the SHA-256 of its size and its first and last 64 KiB (the
  whole file if it is smaller than 128 KiB). It is recomputed only when the size or the
  modification time changes. Reading 128 KiB per new file is negligible, even over SMB.
- A missing file that reappears elsewhere in the same library with the same fingerprint is a
  **move**: it keeps its IDs, and therefore all user data.
- A missing file is only marked (`missing_since`), then forgotten after a **grace period**
  (3 days by default, a runtime setting). If it comes back in time, nothing is lost.
- If a root is unreachable, a folder unreadable, or a root **empty while files were known under
  it** (a mount point that is not mounted), nothing under it is marked missing.

Two exact copies present at the same time remain two files: only the fingerprint of a file that
disappeared can be taken over by one that appeared.

A renamed series folder keeps its item as well. Music is the exception: tracks are identified by
their tags, so retagging creates new items (see [Music, books and photos](media-types.md)).

## Watching folders

A file added to a library should not wait hours for the next scan. Library folders are watched
with [fsnotify](https://github.com/fsnotify/fsnotify) (inotify, ReadDirectoryChangesW, kqueue).
It is pure Go, depends only on `golang.org/x/sys`, and rewriting it would mean maintaining three
system interfaces.

- **A hint, not a truth.** A change under a root triggers a **scan of that library**, which
  compares disk and database as always. Nothing is decided from the event itself, so a lost,
  duplicated or out-of-order event cannot corrupt anything.
- **Quiet time first**: the scan starts after 10 s without a new event, and at most 10 minutes
  after the first one. A long copy is scanned once it is done; a folder moved in one go is
  scanned once.
- **One folder at a time.** fsnotify does not recurse, so every folder under the roots is
  watched (except those the scan ignores) and new ones are added as they appear. On Windows the
  buffer of each folder is reduced to 16 KiB; an overflow simply reports the root as changed.
- **Unreachable roots** (offline share, unplugged disk, too many folders for the inotify limit)
  are logged and retried every 15 minutes. The periodic scan covers for them.
- The `watch_libraries` setting turns it off at runtime.

The periodic scan stays useful: network shares mounted on Linux (SMB, NFS) do not report changes
made from another machine. On Windows, a file Laterna is reading cannot be deleted until it is
released.

Measured on a real disk: 132 folders watched in 90 ms at startup, and a copied episode in the
catalog 10.3 s after the copy ended, of which 10 s are the quiet time.

## Folder picker

Creating a library takes absolute paths as **the server** sees them. The client cannot guess
them: the server often runs in a container on a NAS while the administrator is on a phone. Typing
a path by hand is the first cause of a failed setup.

`LibraryService.BrowseFolders` (administrators, read-only) lists the server's folders; the client
shows the tree and sends back the chosen path.

- **Starting points** (empty path): the drives that exist on Windows; elsewhere `/` and the usual
  mount folders that exist (`/media`, `/mnt`, `/srv`, `/data`, `/volume1`, `/share`).
- **A folder** gives its subfolders in natural order ("Season 2" before "Season 10"), its parent,
  and what media it holds itself.
- **Never file contents**: only folder names and counts. Hidden and system folders are left out
  (`@eaDir`, `#recycle`, `$RECYCLE.BIN`…), and so are Laterna's own directories.
- **Each subfolder is probed**, eight at a time so a slow share does not block everything:
  whether it is readable, whether it has subfolders, and which kinds of media are among its first
  200 entries. That is a hint for choosing the library kind, not a count, and it stays fast on a
  folder of 50,000 photos.
- **Artwork is not a photo** (`poster`, `fanart`, `…-thumb`, `season01-poster`…), and neither are
  images in a folder that holds videos or music. Without these two rules every series folder
  written by Sonarr looked like a photo album.
- Each folder says where it stands relative to existing libraries: it is a library folder, it is
  inside one, or it contains one. The client can warn before the creation is refused for overlap.
- At most 1,000 subfolders (`truncated` beyond).

An administrator can already pick any folder as a library, so seeing folder names exposes nothing
new. Nothing is cached: a folder mounted after startup shows up at once.

## Library order

The administrator chooses the order of libraries (`LibraryService.ReorderLibraries`, which takes
every library exactly once). That order is used by library lists and, inside each group, by the
"latest" rows of the home screen. Without a choice, libraries come by kind (movies, series, music,
books, photos) then by name. A library created later has no rank and comes after those that have
one. Nothing is rewritten when a library is created or deleted (`libraries.position`, 0 meaning
no rank).

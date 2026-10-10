# Requests

A profile asks for a movie, a series, music or a book the libraries do not have, an administrator
approves it, and Laterna hands it to Radarr, Sonarr, Lidarr or LazyLibrarian, then tells the
profile when it is there. Request pages such as Overseerr sign people in through Plex, Jellyfin
or Emby; Laterna has its own accounts, so the requests live in the server.

Laterna still talks to no metadata provider itself: searching goes through Sonarr and Radarr,
which ask TVDB and TMDB, Lidarr, which asks MusicBrainz, and LazyLibrarian, which asks
OpenLibrary.

## Kinds

A search and a destination are for a family: series (Sonarr), movies (Radarr), music (Lidarr) or
books (LazyLibrarian). A result and a request are a series, a movie, an artist, an album or a
book. Series and movies are known by their TVDB or TMDB ID (`external_id`), the others by a text
key (`external_key`): the MusicBrainz ID of an artist or of an album's release group, the
OpenLibrary work ID of a book.

## Search

`SearchRequestable` takes a title and a family and asks its source (`/series/lookup`,
`/movie/lookup`, Lidarr's `/search`, which finds artists and albums together, LazyLibrarian's
`findBook`). Each result says where it stands:

- **available**: an item of a library the profile sees has files and carries the title's ID
  (`provider_ids`: `tvdb`, `tmdb` from the NFO files, `musicbrainz_artist` and
  `musicbrainz_releasegroup` from the tags or `artist.nfo` and `album.nfo`). Books carry no ID
  LazyLibrarian shares: a book is found by its ISBN, or by its title and one of its authors;
- **requested**: a request for it is pending, approved or downloading;
- **tracked**: the source already follows it (another library, or added by hand: a monitored
  series, movie, artist or album, a book LazyLibrarian wants or has), so a request would change
  nothing;
- otherwise it can be requested.

Posters come from TVDB, TMDB, MusicBrainz's cover art and OpenLibrary. Devices do not fetch them
there: the server gives each result a poster address of its own (`/requests/posters/{key}`),
downloads the image once, politely, and serves it from its cache. The key is the hash of a URL a
search returned in the last hour, so the route cannot be used to fetch anything else.

LazyLibrarian cannot look a book up by its ID before it adds it: a book is requested from a search
of the last hour (the server keeps the results), or from the books LazyLibrarian knows.

## Destinations

Where a request lands is set by an administrator, once: a **destination** names a Laterna
library and, on the instance, a root folder, a quality profile and, for Sonarr, the series type
(standard, anime, daily), for Lidarr the metadata profile (which kinds of releases an artist's
albums include). A server with anime and a child's shows in two libraries has two series
destinations. LazyLibrarian decides where books go: a book destination only names the library.

A profile only sees the destinations of libraries its account may browse, and picks one when
there are several. Without a destination for a family, it cannot be requested.

## Who may request

Every account may, unless an administrator takes the right away (`deny_requests`). Then:

- an administrator's requests are approved at once;
- an account marked `auto_approve_requests` too, except from a restricted profile (child or
  controlled), whose requests always wait;
- the others wait for an administrator, who approves them (and may change the destination or the
  seasons) or declines them with a reason.

An account may make at most `request_quota` requests in seven days (10 by default, 0 for no
limit), whatever becomes of them; administrators have no quota.

## What is requested

- A **movie** is requested as such.
- For a **series** the default is the whole series; the first season, the latest season or a list
  of seasons can be asked for instead. Sonarr then monitors those seasons and new episodes as they
  air.
- For an **artist** the default is every album (those its metadata profile keeps), and the next
  ones as they come out; the first or the latest album can be asked for instead.
- An **album** is requested alone: its artist is added to Lidarr if needed, monitored but with none
  of its other albums.
- A **book** is requested as an ebook: LazyLibrarian marks it wanted and searches for it.
  Audiobooks are not requested: no library plays them yet.

## After approval

The job `request.submit` hands the request to its source: Sonarr, Radarr and Lidarr add the title
monitored, with a search for what is missing (a title the instance already has but does not
monitor is monitored and searched instead); LazyLibrarian adds the book, marks it wanted and
searches for it. A refusal fails the request at once; another error is retried like any job, and
one that still fails leaves the request **failed** with the reason. An administrator can approve
it again.

While requests are on their way, the job `requests.refresh` reads every five minutes (and right
after each import a webhook reports) where they stand: Sonarr's, Radarr's and Lidarr's queues
(Lidarr's album by album), LazyLibrarian's book status (`Snatched` while downloading, `Have` once
filed), then **available** as soon as the scan has put the title in the catalog. A series or an
artist is available from its first episode or track, and the request keeps counting what arrives
(episodes, or tracks with a file against those Lidarr monitors) for 90 days.

LazyLibrarian has no webhook: the folder watch notices the books it files, and the periodic scan
catches the rest.

States: pending, approved, downloading, available, declined, failed. The requester can cancel a
pending request; an administrator can delete any.

Each change is announced to the requester and to the administrators (`RequestsChanged`), and
recorded in the activity log. What matters to someone who is not looking (a request that waits,
its approval, its refusal, its arrival, its failure) also makes a
[notification](notifications.md).

## Not in this version

Audiobooks, requests for a better quality of something already there, and votes.

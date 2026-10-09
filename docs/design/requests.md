# Requests

A profile asks for a movie or a series the libraries do not have, an administrator approves it,
and Laterna hands it to Radarr or Sonarr, then tells the profile when it is there. Request pages
such as Overseerr sign people in through Plex, Jellyfin or Emby; Laterna has its own accounts, so
the requests live in the server.

Laterna still talks to no metadata provider itself: searching goes through Sonarr and Radarr,
which ask TVDB and TMDB.

## Search

`SearchRequestable` takes a title and a kind (series or movie) and asks the instance for that kind
(`/series/lookup`, `/movie/lookup`). Each result says where it stands:

- **available**: an item of a library the profile sees carries its TVDB or TMDB ID
  (`provider_ids`, from the NFO files) and has files;
- **requested**: a request for it is pending, approved or downloading;
- **tracked**: the instance already has it (another library, or added by hand), so a request
  would change nothing;
- otherwise it can be requested.

Posters come from TVDB and TMDB. Devices do not fetch them there: the server gives each result a
poster address of its own (`/requests/posters/{key}`), downloads the image once, politely, and
serves it from its cache. The key is the hash of a URL a search returned in the last hour, so the
route cannot be used to fetch anything else.

## Destinations

Where a request lands is set by an administrator, once: a **destination** names a Laterna
library and, on the instance, a root folder, a quality profile and, for Sonarr, the series type
(standard, anime, daily). A server with anime and a child's shows in two libraries has two series
destinations.

A profile only sees the destinations of libraries its account may browse, and picks one when
there are several. Without a destination for a kind, that kind cannot be requested.

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

A movie is requested as such. For a series the default is the whole series; the first season,
the latest season or a list of seasons can be asked for instead. Sonarr then monitors those
seasons and new episodes as they air.

## After approval

The job `request.submit` adds the title to the instance, monitored, with a search for what is
missing. A title the instance already has but does not monitor is monitored and searched instead.
A failed attempt is retried like any job; one that still fails leaves the request **failed** with
the reason, and an administrator can approve it again.

While requests are on their way, the job `requests.refresh` reads the instance's queue every five
minutes (and right after each import its webhook reports): progress, then **available** as soon as
the scan has put the title in the catalog. A series is available from its first episode, and the
request keeps counting the episodes that arrive until all those monitored are there.

States: pending, approved, downloading, available, declined, failed. The requester can cancel a
pending request; an administrator can delete any.

Each change is announced to the requester and to the administrators (`RequestsChanged`), and
recorded in the activity log.

## Not in this version

Music (Lidarr) and books (LazyLibrarian), requests for a better quality of something already
there, and votes.

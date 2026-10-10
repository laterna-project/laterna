# Home, history and recommendations

## The server decides the home screen

When each client builds its own home screen from a handful of queries, home screens differ from
one device to the next and every improvement needs every client updated. In Laterna,
`HomeService.GetHome` returns ordered rows. Each has a kind (`HomeRowKind`), a title and its
items, using the catalog's messages. Clients display the rows in the order received.

| # | Group | Rows |
|---|---|---|
| 1 | In progress | resume, next up, continue reading |
| 2 | New to watch | latest additions of each movie and series library |
| 3 | Coming soon | what Sonarr, Radarr and Lidarr expect (see below) |
| 4 | Recommendations | recommended for you, then up to two "because you watched…" |
| 5 | Music | recently played albums, then latest additions of each music library |
| 6 | The rest | latest books, then latest photos |

- Empty rows are left out.
- Inside a group, libraries follow the order chosen by the administrator
  (see [Libraries](library.md)). Groups are fixed on purpose: the home screen means the same
  thing on every server.
- **Resume**: movies and episodes that are present and have a position, most recent first.
- **Next up**: for each series in progress, the present unwatched episode that follows the last
  watched one (specials excluded), most recently followed series first. An episode already
  started is in "Resume", not here.
- **Latest series** are ordered by the date of the last episode received. Episodes are read by
  date added, in index order and in batches, until enough visible series are found.
- Titles are composed text (`home.…` keys) that the client can translate
  (see [Internationalization](i18n.md)).

`GetHome` is the heaviest call of the API and the one that sets the worst p95 in `task perf`
(around 20 ms on the synthetic catalog).

## Coming soon

Sonarr, Radarr and Lidarr know what the libraries will get next. The row "Coming soon" lists the
episodes, movies and albums they monitor, expect within two weeks and do not have yet, soonest
first. An episode that aired less than a day ago and has not arrived is still listed: it is on its
way.

- Nothing of it is in the catalog, so the row has no items: its entries are `UpcomingRelease`
  messages in `HomeRow.upcoming`. A client that does not know them would show an empty row, so the
  row is only sent when the client asks (`GetHomeRequest.upcoming`).
- A movie counts on the day it comes out at home (digital, else physical). Its day in theaters
  brings nothing to a library. Movies and albums have a day, not a time (`all_day`).
- The job `upcoming.refresh` reads the calendars every 30 minutes, when an integration is linked or
  unlinked, and a minute after each import a webhook reports. The result is kept in memory:
  `GetHome` never waits for an instance, and an instance that does not answer keeps its last
  answer.
- An entry links to the series or the artist when the catalog has it, with its images. Otherwise
  the server serves the poster the instance names, through the route of request posters.

A profile only sees what will land where it may look:

- the library is the one of the series or the artist in the catalog; else the one a request
  destination gives to the root folder on the instance; else any library of that kind, and the
  profile then has to be allowed in all of them;
- parental control uses the rating the catalog shows for the series, else the one the instance
  gives. Music has none.

## Resume rules

`domain.Progress` decides what a reported position means:

- before 5% of the duration or before one minute: nothing to resume;
- after 90%: finished (played, one more play, resume position cleared);
- in between: a resume position;
- without a known duration, a playback is never considered finished;
- a music track never has a resume position; past 90% it counts one play.

Books have their own rule: finished at 98% (see [Music, books and photos](media-types.md)).

## Play history

Watch data (`user_data`) only holds totals per profile. The history keeps one row per **play**
(`play_history`): a movie, an episode or a track, from the opening of the playback to its close.

- **Time actually played.** At each position the player reports, the advance since the previous
  one counts if it is plausible: at most twice the elapsed time plus 5 s. A seek, a rewind or a
  pause does not count. At the first report, a playback that did not start from the proposed
  resume position is taken to have started from the beginning.
- **Threshold**: a play enters the history beyond one minute played (30 s for a track), or half
  of a shorter item. Below that it was a peek or a false start.
- **Snapshot**: the play keeps the title and the series of an episode (or the artists of a
  track) as they were. If the file is forgotten later, the item reference becomes NULL and the
  title stays: history and yearly recaps survive deletions.
- **Offline plays** enter the history once, even if the report is replayed.
- **Per profile**: each profile sees its history, deletes a play or clears it all. Items stay
  played: the history is a journal, not the watch state.

A playback in progress is only in the history once it closes. Books are not in it: their progress
counts pages, not time.

## Statistics and the yearly recap

`HistoryService.GetStats` covers a calendar year or all time.

- Days, hours and months are counted in the **client's time zone** (`time_zone`,
  "Europe/Paris"). Time zone data is embedded in the binary (`time/tzdata`), since a server on
  Windows or in a minimal container may have none.
- Content: time played in total and per kind; distinct movies, episodes, series and tracks; top
  series, movies, artists, tracks and genres; time per month and per weekday × hour; the busiest
  day; the most episodes of one series in a day; the first and the last play. Rankings carry the
  images of items that still exist.
- **Computed on demand** in a pure package (`internal/stats`). Plays are streamed and reduced to
  what matters; genres are attributed at the end, once per movie or series watched. Nothing is
  precomputed: the history stays the only source, and a deleted play disappears from the
  statistics at once.

On an extreme year of 20,000 plays (about fifty a day, music included): 35 ms of reading and
10 ms of computing. An ordinary year takes under 10 ms. A first version loaded whole rows at once
and allocated 33 MB for the read alone.

## Recommendations

A home server has a handful of profiles. It cannot infer "people who liked this liked that". It
does know, from NFO files, what characterizes each movie and series, and nothing has to leave the
server.

`internal/recommend` is pure logic:

- **One vector per movie or series**, one component per feature: genres, studios, collections,
  directors, writers, the first five actors, the decade.
- **Weight** of a feature: its kind (collection 3, director 2, writer 1.5, actor 1, genre 1,
  studio 0.5, decade 0.3) times its rarity (idf, ln(1 + N/df)). A genre half the catalog shares
  says almost nothing; a director says a lot.
- **Similarity** is the cosine of two vectors.
- **A profile's taste** is the sum of the vectors of what it watched, each weighted by time
  watched (ln(1 + hours)), episodes watched (0.5 × ln(1 + n)), played (+1) and favorite (+2),
  halved every six months since the last time.
- **Recommendation**: the cosine between taste and each title; a good rating barely breaks ties.
  What the profile has watched, started or marked as favorite is never recommended.

What clients see:

- On the home screen, "Recommended for you" and up to two "Because you watched …" rows
  (`source_item_id`), for the latest movies or series watched long enough. A title appears in
  one row only. The "because" rows take the titles closest to their source first, which is what
  they announce; "Recommended for you" takes the rest. The other way round, the most telling
  titles went into "Recommended" and the explanation no longer held.
- `CatalogService.ListSimilar` for a detail page. An episode or a season stands for its series.
- Everything goes through the visibility filter of the profile.

Cost:

- The index lives in memory, shared by all profiles, and is rebuilt when the catalog changes, at
  most once a minute.
- Each profile's taste is cached and dropped at every play, "played", favorite or history
  deletion (ten minutes at most).
- With about 3,000 movies and series: a few milliseconds added to `GetHome`, and `ListSimilar`
  in under 3 ms.

Limits: recommendations are worth what the NFO files are worth. With no genres and no cast, a
title is close to nothing and is never recommended. Music is not covered: its tags give little
more than genres. Nothing is learned from one profile to another.

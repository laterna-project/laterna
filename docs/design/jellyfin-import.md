# Importing from Jellyfin

Leaving Jellyfin should cost users nothing: their accounts, what they watched, where they
stopped, their favorites and, as far as possible, their history. Laterna has **no compatibility
with the Jellyfin API** (see [API](api.md)): only its data is read.

Since version 10.11, Jellyfin keeps everything in one SQLite database (`data/jellyfin.db`). The
useful tables are `Users` (with `Permissions` and `Preferences`), `BaseItems`, `AncestorIds`
(library membership), `UserData`, and `ActivityLogs`, which records every playback start and stop
in UTC.

## Read a copy, never Jellyfin's database

`internal/jellyfin`:

- The administrator gives Jellyfin's **data folder** as the Laterna server sees it: `/config` of
  the official image, mounted read-only into Laterna's container if needed. The `data` folder or
  the `jellyfin.db` file itself work too. The [folder picker](library.md#folder-picker) helps.
- The database and its WAL are copied to a temporary folder in the cache, then read with the
  SQLite driver already embedded. Jellyfin can keep running: no lock is taken on its side.
- A copy taken during a write can be inconsistent. `PRAGMA quick_check` rejects it; try again,
  or stop Jellyfin for the import.
- A database from before 10.11 (`library.db`) or with an unexpected schema is refused with the
  reason.
- IDs are normalized (Jellyfin writes them sometimes uppercase with dashes, sometimes without).
- Several `UserData` rows for one item are merged into one.

The copy contains Jellyfin's password hashes. It is deleted as soon as it has been read.

## Two calls

`ImportService` (administrators):

1. `PreviewJellyfinImport` says, for each user, the proposed target and what would be matched,
   then the Jellyfin files missing from Laterna, with examples, to understand a path that does
   not match.
2. `ImportJellyfin` imports everything, with the targets corrected if needed.

Both re-read the database: nothing is kept between them. On a server with a few users and a few
thousand rows, each takes a fraction of a second.

## Targets

Each Jellyfin user goes to one of:

- **The account of the same name** (case-insensitive), proposed if it exists: its profile of the
  same name, otherwise its first unrestricted profile.
- **A new account**, proposed otherwise, with its profile:
  - administrator, disabled and download flags are carried over;
  - libraries: those of Laterna that hold the files of the user's Jellyfin libraries;
  - parental control: Jellyfin's maximum rating is an age since 10.11; blocked unrated content
    becomes "hide unrated";
  - **password**: the PBKDF2 hash from Jellyfin stays valid and becomes an argon2id hash at the
    first successful sign-in. A user without a password (allowed there) gets a random one for
    the administrator to replace.
- **A new profile of an existing account.** This is how a family that had one Jellyfin user per
  person becomes profiles of one account; parental control goes on the profile.
- **An existing profile**, or **nothing**.

## Watch data

- **Matching by path.** Both servers read the same files, rarely under the same path
  (`/media/tv/…` in one container, `D:\media\tv\…` on the other). A file is matched by the
  longest common path suffix among files with the same name: at least the parent folder if there
  are several, and nothing in case of a tie.
- TMDb or TVDB IDs are not used: the path is enough and never picks the wrong version.
- A file with several episodes gives its data to each. A favorite on a series, a season or an
  album goes through one of its files.
- **Merging** with what the profile already has: played or favorite if either says so, the
  larger play count, the position of the most recent playback. Dates are rounded to the
  millisecond as in Laterna's database; without that, a second import rewrote everything, because
  Jellyfin's sub-microsecond date always looked more recent than the same date re-read.

## History

- A play is a playback start in the activity log followed by the stop of the same user on the
  same item within 24 hours.
- A new start of the same item less than an hour later, with no stop in between, continues the
  play: Jellyfin logs one at every stream restart.
- **Time played**: the log does not say where playback stopped. The time played is the time
  elapsed between start and stop, capped at the item's duration (pauses included: an estimate,
  recorded with the device "Jellyfin"). The play is finished beyond 90%.
- The usual history rules apply: under a minute is not a play.
- An item missing from Laterna keeps its title, like a forgotten item. An item gone from
  Jellyfin has no reliable title and its play is skipped.
- Each play keeps a key (Jellyfin user and log entry), unique per profile: a second import does
  not copy it again.

The Playback Reporting plugin keeps a more precise history, but it is a plugin and its dates are
in the server's local time with no zone, so the activity log is used instead.

## Results and limits

- Tried on a real Jellyfin 10.11 server: users mapped to an administrator profile and two new
  profiles, nearly every item matched, a few hundred plays added, "next up" and the yearly recap
  picking up where Jellyfin left off. A second and a third run change nothing.
- An end-to-end test runs on a small Jellyfin database written in Jellyfin's own formats
  (`jellyfintest`).
- Imported history is an estimate, pauses included. Plays recorded by Laterna are exact.
- Only Jellyfin 10.11 and later is supported; upgrade an older server first.
- Emby and Plex are not planned. The `internal/jellyfin` package is the model for another
  importer.

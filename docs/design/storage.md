# Storage

## SQLite, and only SQLite

A self-hosted server should not need an external database. Laterna uses SQLite through
`modernc.org/sqlite` (pure Go, FTS5 included), in WAL mode, with `STRICT` tables. There is one
engine and no PostgreSQL option "just in case": the user configures nothing, and a backup is one
file.

- **One writer, many readers.** A single write connection runs every write inside
  `store.Write(ctx, func(q store.Q) error)`, a `BEGIN IMMEDIATE` transaction, so a transaction
  never fails halfway on a lock conflict. Reads use a pool of `query_only` connections
  (`store.Read()`).
- **goose** for migrations: SQL files embedded in the binary and applied at startup. The same
  files are the schema **sqlc** reads to generate typed Go for static queries
  (`internal/store/queries/*.sql` → `internal/store/sqlc`).
- **A small query builder** (`store/query.go`) for lists with dynamic filters, which code
  generation serves badly. Every value is a bound parameter, and the number of placeholders is
  checked.
- `store.Q` translates rows into domain types. Nothing above the store sees SQL types.

Schema conventions:

- IDs are `BLOB` (16 bytes), and a `BLOB` is always an ID;
- dates are `INTEGER` Unix milliseconds;
- booleans are `INTEGER` 0/1 with a `CHECK`;
- hashes and hashed secrets are `TEXT`;
- names that must be unique regardless of case have a normalized `*_key` column.

SQLite cannot widen a `CHECK` constraint, so a migration that adds a kind of library or item
rebuilds the table. Those migrations turn foreign keys off first and stop if they are still on:
dropping the old table would otherwise cascade and delete everything.

## A read never holds two connections

A store read never asks for a second connection while the first is still open. It reads its rows
to the end, then runs the next query. An early version of the "latest series" row kept a cursor
open on one connection while filtering through another; under parallel load every call held one
connection while waiting for another, and the server deadlocked. A test now checks that the
whole home screen works with a single read connection.

## Lists that stop at the last row

List pages must not sort a whole library to return fifty rows.

- **`items.present`** says whether an item has a file that is present. It is maintained by
  triggers on `item_files` and on `media_files.missing_since`, so no code path can forget it.
  Series, seasons, artists, albums, book series and photo albums derive presence from their
  children.
- Indexes on `(kind, present, sort_title, id)` and `(kind, present, added_at, id)`, with and
  without `library_id` in front, give the full order: the page stops at its last row. A test
  asserts the query plans.
- **Optimizer statistics** (`PRAGMA optimize`) matter: without them SQLite picked the index of
  all episodes to list one series (2.3 ms instead of 0.05 ms). They are refreshed when the
  database opens, by a low-priority job after a scan that changed files, and daily.
  `analysis_limit` bounds the cost.

`items.present` is derived data. A new kind of item that has files gets it for free; a kind that
is derived from its children must be added to the presence conditions of the catalog.

The search index is an external-content FTS5 table tied to the implicit rowid of `items` and kept
up to date by triggers. Table rebuilds keep the rowid for that reason.

## Memory

At rest, the server's memory is the Go heap (a few MB live), SQLite's memory and mapped code.
SQLite's page cache was the part that broke the budget: each read connection keeps up to 2 MB of
pages by default, there is one connection per CPU, and the C allocator inside the pure-Go driver
keeps what SQLite frees instead of returning it to the system. Closing idle connections changes
nothing.

The fix is to bound what gets allocated: **512 KiB of page cache per read connection**. The
writer keeps the default for scans. Pages beyond the cache come back from the operating system's
file cache. Under four parallel clients the worst p95 moved from 14 to 16 ms, and memory after
load dropped from 77 to 51 MB.

## Backups

Everything the server remembers lives in the database: accounts, profiles, watch state, history,
playlists, settings, themes. Copying the file of a database open in WAL mode gives an
inconsistent copy, so backups are real hot backups.

- **What is backed up**: the database, and nothing else. Downloaded images, scrubbing
  thumbnails, extracted subtitles and audio fingerprints can all be rebuilt from the media. A
  backup is therefore small (around 10 MB for a catalog of a thousand items) and cheap to keep
  in several copies.
- **How**: `VACUUM INTO` (`store.Backup`). Consistent, compacted, taken while the server runs;
  reads continue and writes wait a fraction of a second. The copy is written under a temporary
  name, checked (`quick_check`, schema version), then renamed: there is never a half-written
  backup.
- **Where**: `backups` under the data directory, or `paths.backups` / `LATERNA_BACKUP_DIR`.
  Putting it on another disk protects against losing the first one.

| Kind | When | Kept |
|---|---|---|
| `auto` | Nightly job, at most one per 20 h | `backup_keep` setting (7 by default, 0 turns them off) |
| `migration-v<N>` | When opening an existing database with a pending migration | the last 3 |
| `manual` | `SystemService.CreateBackup` or `laterna backup` | until deleted |
| `before-restore` | The database a restore replaces | the last 3 |

The backup taken before a migration is the valuable one: an upgrade is when a database is most
at risk and when nobody thinks of copying it.

### Restore

An open database cannot be swapped, so a restore has two steps.

1. **Stage**: `SystemService.RestoreBackup`, or `laterna restore <backup>` when the server no
   longer starts. The backup is checked (readable, at a schema version this binary can open) and
   copied next to the database. `CancelRestore` undoes it.
2. **Apply**: at the next start, before the database opens (`store.ApplyRestore`). The current
   database is kept among the backups under its own name; if it is unreadable, which is often the
   reason for restoring, it is kept as it is. A staged restore that fails its checks is set
   aside and the server starts on its current database.

A backup older than the binary is migrated when it opens, as usual. A newer one is refused.

Backups are for administrators only. `GET /backups/{name}` needs the token of an administrator on
an unrestricted profile, because a backup holds everything, password hashes included. Only files
named like a backup are listed, served or deleted. The metric
`laterna_backup_last_timestamp_seconds` lets you alert on a backup that is too old.

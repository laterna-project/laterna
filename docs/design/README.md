# Design notes

These notes explain why Laterna is built the way it is: the choices that shape the code, what was
tried and dropped, and the limits we know about. They describe the current state of the server,
not its history. When a change contradicts a note, update the note in the same change.

| Note | What it covers |
|---|---|
| [Architecture](architecture.md) | Go without cgo, package layers, IDs, configuration, background jobs, testing, performance budgets |
| [API](api.md) | The Protobuf contract, access levels, errors, catalog messages, paging, search, images, events |
| [Storage](storage.md) | SQLite, one writer and many readers, migrations, list queries, backups and restore |
| [Libraries](library.md) | Scanning, file identity, folder watching, the folder picker, library order |
| [Metadata](metadata.md) | NFO files and local artwork, Sonarr and Radarr, collections |
| [Playback](playback.md) | Direct play, HLS, transcoding, HDR, FFmpeg processes, scrubbing thumbnails, offline downloads |
| [Subtitles](subtitles.md) | Extraction at import, formats, fonts, burn-in |
| [Intro and credits detection](intro-detection.md) | Named chapters and audio fingerprints |
| [Accounts](accounts.md) | Accounts, profiles, sessions, parental controls, device login, passkeys, OpenID Connect, abuse limits |
| [Home, history and recommendations](home.md) | Home rows, resume rules, play history, statistics, recommendations |
| [Music, books and photos](media-types.md) | The non-video libraries, collections and playlists |
| [Watch parties](watch-party.md) | Shared playback state and how clients stay in sync |
| [Internationalization](i18n.md) | Error codes, composed text, languages |
| [Themes](themes.md) | Design tokens, contrast checks, per-profile choice |
| [Operations](operations.md) | Runtime settings, administration, logs, activity, metrics, traces |
| [Local discovery](discovery.md) | DNS-SD announcement on the local network |
| [Importing from Jellyfin](jellyfin-import.md) | Users, watch state and history from a Jellyfin database |

# Operations

An administrator should be able to see and tune the server without a shell and without a
restart. Everything here is for administrators on an unrestricted profile.

## Runtime settings

Startup configuration only holds what is needed before the database opens (see
[Architecture](architecture.md)). Everything else is a setting stored in the database
(`domain.Settings`), loaded at startup and applied the moment it changes
(`SystemService.UpdateSettings`).

| Setting | Default | Notes |
|---|---|---|
| Server name | the host name | Announced to clients |
| Language | `en` | For server-written text when the request and the profile ask for nothing the server knows |
| Download images | on | Off means no outgoing request at all |
| Scan interval | 6 h | 15 min to 7 days, or 0 for never |
| Missing-file grace | 3 days | 1 h to 90 days |
| Scrubbing thumbnails | on | |
| Detect intros by audio | on | Named chapters are always used |
| Watch library folders | on | |
| Max video transcodes | 0 (automatic) | |
| Backups kept | 7 | 0 turns nightly backups off |
| Public URL | empty | Sets the passkey relying party and the OIDC redirect address |
| Web URL | empty (the public URL) | Where the web client lives; used for device login |
| Passkey RP ID, extra origins | empty | |

A setting that changes scheduling re-arms its timer at once; one that changes folder watching
resyncs the watchers.

## System status, jobs and tasks

`SystemService` gives:

- **status**: version and commit, start time, operating system, the first line of
  `ffmpeg -version`, the encoders, tone mappers and GPU chain that were detected, directories,
  current playbacks and FFmpeg processes, transcodes in use against the limit, jobs pending,
  running and failed;
- **the job queue**: counts per kind and state, and the 200 most recent failed jobs with their
  error and their target **in plain words** (file path, title, library name, image address). A
  failed job can be retried or dismissed;
- **tasks on demand**: scan every library, run the purges;
- backups (see [Storage](storage.md)), the OpenID Connect provider, metrics.

## Logs

- Output goes to stdout (text or JSON) and to daily files `laterna_YYYYMMDD.log`, kept 14 days.
- `logging.Ring` keeps the last 2,000 messages in memory. They can be filtered by level and by
  text, in the message or its attributes.
- Daily files can be listed and downloaded with `GET /logs/{name}` and an administrator's token.
  The name must match the pattern and the file is opened through `os.Root`, so no arbitrary path
  can be reached.

The in-memory log follows the log level: at `info`, debug messages are not in it.

## Activity log

The `activity` table keeps 90 days of what happened, purged daily:

- setup, successful and refused sign-ins (unknown account, wrong password, disabled account)
  with the device and address;
- accounts created, changed, deleted, and devices signed out by an administrator;
- libraries created, changed, deleted, and scans that changed something;
- the start and end of playbacks (who, what, on which device, how), watch parties;
- settings, integrations and their webhooks, backups;
- jobs that failed for good.

Each entry is a composed text (see [Internationalization](i18n.md)) that holds the names, so a
deleted account stays readable. Refused sign-ins and failed jobs are **warnings** that can be
isolated. An entry that cannot be written never blocks the action it describes.

`ActivityService` gives the paged activity log (filterable by account and by warnings only), the
signed-in devices of every account, which can be signed out, and current playbacks (who, which
profile, which device, which title, how, position), which can be stopped.

## Metrics

`GET /metrics` serves the Prometheus text format. It is written in the tree (`internal/metrics`):
the official client library brings about ten modules and their memory, against a 60 MB budget at
rest.

**Closed by default.** A home server is often reachable from the Internet and its metrics say a
lot about it (number of accounts, size of the catalog, activity). Without a token, `/metrics`
answers 404. An administrator opens it with `SystemService.EnableMetrics`, which returns a
dedicated read token **once**; only its hash is stored, and a new token replaces the old one.
`DisableMetrics` closes it. A session token does not open `/metrics`.

```yaml
scrape_configs:
  - job_name: laterna
    authorization:
      credentials: <the token>
    static_configs:
      - targets: ["laterna.example.org:8096"]
```

What is measured:

- **HTTP**, by route: the `ServeMux` pattern ("GET /images/{id}/{hash}"), never the address, so
  the number of series stays bounded. Requests by status class (499 when the client left),
  duration, bytes served.
- **API**, by procedure and Connect code: calls, and duration of unary calls.
- **Playback**: opened by method (direct, remux, transcode, converted) and in progress, FFmpeg
  runs in progress, transcodes against the limit.
- **Background jobs**: queue by kind and state, runs by kind and outcome, duration.
- **Catalog**: present items by kind.
- **Process**: real resident memory (`/proc/self/statm` on Linux, the working set on Windows;
  SQLite's cache lives outside the Go heap, so the heap alone would lie), Go memory, threads,
  garbage collector.
- Event subscribers, database and WAL size, the last backup, the version.
- Activity entries by kind (sign-ins, failed sign-ins, playbacks, scans…), which covers a lot
  without instrumenting each place.

No measurable effect on `task perf`.

## Traces

Traces are exported with OpenTelemetry's OTLP/HTTP JSON, which any collector accepts on port 4318
(Jaeger, Tempo, Grafana Alloy, the OpenTelemetry Collector). The exporter is written in the tree
(`internal/telemetry`); the official SDK would bring gRPC and about forty modules.

It is configured with the standard variables: `OTEL_EXPORTER_OTLP_ENDPOINT` (or
`…_TRACES_ENDPOINT`), `OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_SERVICE_NAME`,
`OTEL_RESOURCE_ATTRIBUTES`, `OTEL_TRACES_SAMPLER` and `OTEL_TRACES_SAMPLER_ARG`,
`OTEL_SDK_DISABLED`. An invalid value stops startup, like the configuration. Only `http/json` is
supported. **Without a collector everything is off**: no span, no cost.

- Spans are linked through the context and propagated with the W3C `traceparent` header, in and
  out.
- Root traces are sampled by trace ID ratio; others follow their parent.
- Export is batched (512 spans, every 5 s). A full queue drops the extra spans and says so at
  shutdown. An unreachable collector slows nothing down.

What is traced:

- **every HTTP request**, named after its route or, for the API, its procedure
  (`laterna.v1.CatalogService/GetMovie`), never after the address, which sometimes carries a
  secret. It is marked as an error only for a server fault (5xx, `internal`, `unknown`,
  `unavailable`, `data_loss`): an item not found is not an outage. The access log carries the
  `trace_id`;
- **every background job run**, as the root of its own trace;
- **every FFmpeg run**, as a child of the segment request that started it, with the method, the
  encoder and whether the GPU is used;
- **every outgoing call** (Sonarr, Radarr, NFO images, OpenID Connect), as a child of the request
  or job that makes it.

## Shutdown

On SIGINT or SIGTERM the server stops announcing itself on the local network, stops accepting
requests, stops background work and waits for it, then closes the database. The delay is 15 s.
An open stream depends on the request context, which is cancelled at shutdown, so it does not
hold the server up. FFmpeg children die with the server (see [Playback](playback.md)).

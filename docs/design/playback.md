# Playback

Laterna assumes nothing about the hardware it runs on (an NVIDIA card, an Intel or AMD chip, a
Raspberry Pi, a Mac, a server with no GPU in Docker) and nothing about the media. Each device
declares what it can play, and the server decides from that profile and the source.

## FFmpeg: stock, and a child process

- **Stock FFmpeg**, 7.1 or later. The Docker image, CI and the release bundles use one pinned,
  checksummed static GPL build from BtbN (`packaging/ffmpeg.lock`). No fork is required.
- The server uses the `ffmpeg` and `ffprobe` it is configured with, otherwise the ones shipped
  next to its own binary, otherwise the ones in `PATH`.
- FFmpeg and ffprobe run as **child processes**, never linked into the binary. A crash in FFmpeg
  does not take the server down, and its GPL license stays on its side of the process boundary.
- **Capabilities are probed at startup with real attempts** (encoders, tone mappers, the GPU
  chain), each with a fallback. What a build *announces* is not trusted.
- Arguments are built by typed code and passed as an array, never through a shell. Inputs are
  prefixed with `file:` so a file name cannot be read as a protocol.

`internal/proc.Command` is the only way to start one of these processes, and it ties the child to
the server:

- on Windows, the server puts itself in a job object that kills its members when it closes
  (`proc.Bind`); children join automatically, and the system closes the object however the server
  dies;
- on Linux, each child gets SIGKILL when the server dies (`Pdeathsig`);
- on macOS there is no equivalent without outside help: only a normal stop ends the children.

A test restarts the test binary as a server and then as a child, kills the server and fails if
the child survives.

## Deciding how to play

`playback.Decide` is pure logic: device profile and source in, plan out. **Each stream is
decided on its own.**

- **Direct play** when the device can play the container and the streams: the file is served
  with `http.ServeContent` (Range, ETag).
- Otherwise **HLS in fMP4**. A stream the device can play, and that fits in fMP4, is copied
  (remux). A stream it cannot play is transcoded to a target that plays everywhere: **H.264**
  8-bit High profile for video, **AAC** for audio (5.1 at most). Only the audio is transcoded
  under a HEVC picture the device can play; only the video under an audio stream it can play.
- A device without HLS, or one that cannot play the target, gets a refusal with the reasons.
- Music never uses HLS; see [Music, books and photos](media-types.md).

`StartPlayback` returns the method, the reasons, which streams are transcoded and with which
encoder: enough for a client to show "playback info".

## HLS aligned on keyframes

This was the riskiest part of the project: cut a file into segments without re-encoding, seek
anywhere at once, and keep timestamps continuous from one segment to the next, including when
the next segment comes from an FFmpeg restarted after a seek.

- The playlist is a complete **VOD playlist** from the start (`#EXT-X-PLAYLIST-TYPE:VOD`, init
  segment through `#EXT-X-MAP`): a full seek bar and immediate seeking. It plays in hls.js,
  Media3 and AVPlayer.
- A **keyframe index** (presentation times) is built for each video at import: read straight from
  Matroska Cues when they exist (a few kilobytes), otherwise by ffprobe (a full read of the file).
  It is stored in the database.
- **Segment boundaries**: the first keyframe reached at least 6 s after the previous boundary.
  The cutting depends only on the source.
- **One FFmpeg per "run"**, starting at the requested segment and writing a single continuous
  fMP4 stream to a pipe, one fragment per keyframe, with the source timestamps:

  ```
  -copyts -ss <start> -i file:… -map_chapters -1 -map_metadata -1 -c copy
  -avoid_negative_ts disabled
  -movflags +frag_keyframe+empty_moov+default_base_moof+frag_discont+negative_cts_offsets
  -f mp4 pipe:1
  ```

  (`-tag:v hvc1` is added for HEVC, which Safari and AVPlayer require.) Signed composition
  offsets keep the presentation times of the source without an edit list; without them a video
  with B-frames shows two frames late relative to the sound.
- **Laterna sorts the fragments itself** (`internal/media/fmp4`). The init segment is kept once.
  Each fragment (`moof` + `mdat`) goes to the segment that the presentation time of its first
  video frame points to, and its sequence number is rewritten. **A segment has the same content
  and the same times whichever run produced it.**
- **Seeking** outside what has been produced starts a new run at the target segment. FFmpeg
  starts from the index point before it, and earlier fragments are dropped.
- A run stops when it is 8 segments ahead of the player, and resumes on demand.

What the prototype ruled out:

- one FFmpeg per segment with `-ss`/`-to`: in copy mode the cut is not packet-exact, and
  segments overlap;
- the `segment` muxer with `-segment_times`: each segment restarts at zero;
- the `hls` muxer: its boundaries depend on where the run started, so they change after a seek.

In remux, the first segment is available in well under a second, including after a seek. Open
GOPs (HEVC with CRA frames) leave a few frames undecodable right after a seek; players skip them.

## Transcoding

Transcoded video uses the same stream, the same fragment sorting and the same runs. The
differences:

- **The encoder is picked by real attempts**, once, in the background at startup. Every H.264
  encoder announced by `ffmpeg -encoders` encodes one second of test pattern with the playback
  settings. Those that succeed are kept in this order: `h264_nvenc`, `h264_qsv`, `h264_amf`,
  `h264_vaapi`, `h264_videotoolbox`, `h264_v4l2m2m`, `h264_mf`, then `libx264`, always present in
  GPL builds, as the last resort. A missing card or driver just means the next one takes over.
  `ffmpeg.encoder` / `LATERNA_FFMPEG_ENCODER` forces one (an unknown name is refused at startup;
  one that does not work on the machine falls back to the best, with a warning).
- Video is scaled to 1080 lines at most and converted to 8 bits. Quality targets roughly x264
  CRF 21 to 23.
- **Fixed 6 s segments.** A transcoded stream chooses its own keyframes, so an IDR is forced at
  the start of each segment with `-force_key_frames expr:gte(t,n_forced*6)`. The `t` in that
  expression counts from the **first encoded frame**, even with `-copyts`: an expression in
  absolute time stops forcing anything after a seek. (That trap produced a 25 s segment, three
  segments never produced, and a stuck player.) If the leftover at the end is shorter than half
  a segment, it extends the previous one rather than making a segment with no picture.
- **No B-frames** (`-bf 0`). Decode order is display order, and decode timestamps no longer
  depend on the first frames of a run. With B-frames, two segments from different runs could
  follow each other with a decode timestamp going backwards, and a frame was lost at the joint.
  The cost is a slightly higher bitrate at equal quality.
- **Source timestamps** (`-fps_mode passthrough`): no frame duplicated or dropped, variable
  frame rate included.
- **Copied audio under transcoded video** needs `-copypriorss:a 0`. FFmpeg reads from the source
  keyframe before the run's start; the video decoded before the start is discarded, but copied
  audio was not, which gave seconds of duplicate sound.
- **Transcoded audio is shifted by the AAC priming** (`-af asetpts=PTS+1024/SR/TB`). FFmpeg's AAC
  encoder adds 1024 samples of priming, stamped before the start of the run. With source
  timestamps kept, the run that starts at the beginning produced a negative audio `tfdt`, which
  Media3 refuses and hls.js tolerates. The audio is delayed by the priming (21 ms at 48 kHz) in
  every run alike, so segments stay identical from one run to the next.
- **No waiting forever.** A run that passes a segment without producing it says so; asking for
  that segment starts a run at it. If a run started at that very segment does not produce it
  either, it does not exist in the stream and the request fails at once.
- A run that fails on the GPU before its first frame restarts on the CPU, and that playback
  stays there.

Tests check every encoder usable on the machine that runs them, not only the preferred one:
keyframes on every boundary whatever the run's start, and a stream stitched after a seek with no
frame lost or repeated. CI has no GPU and checks `libx264`.

## HDR to SDR

An HDR picture (PQ, HLG) that has to be transcoded to 8-bit H.264 is tone mapped. An HDR picture
copied to an HDR device stays HDR, and direct play never tone maps.

Each method is **tried at startup** with the chosen encoder on one second of HDR10 test pattern,
in order of preference:

1. libplacebo (Vulkan, BT.2390, also handles Dolby Vision);
2. OpenCL (`tonemap_opencl`);
3. VAAPI;
4. zscale on the CPU, present in every GPL build, as the last resort.

Tone mapping happens after scaling: four times fewer pixels to convert for a 4K source. The
output is tagged BT.709. If no method works, an HDR picture that would need transcoding is
refused with a clear reason rather than shown washed out.

**The GPU chain**, when it passed its test at startup: Vulkan decoding, then scaling and tone
mapping by libplacebo on frames that never leave the card; only the reduced frames come back to
memory. A stream the card cannot decode is decoded by FFmpeg on the CPU and libplacebo accepts
those frames too, so one test is enough. The chain is not used when a subtitle has to be burned
in, since the overlay happens in memory.

Measured on a desktop with a mid-range NVIDIA card, on a 4K AV1 HDR10 film: first frame in about
3 s, seeks in 2.3 to 2.4 s, and a 6 s segment produced in 0.55 s once running. A 1080p 10-bit
HEVC source transcodes with seeks in 1.3 to 1.8 s. Remux seeks take under a second. Most of the
time of a transcoded seek is decoding from the previous source keyframe, which can be 10 s back.

## Sessions

A playback session lives in memory. Its ID and a secret form the stream URLs, and the secret
stands in for authentication, since a `<video>` element sends no header.

- A watchdog stops a run after 30 s without a request and closes the session after 30 minutes.
- Progress is saved every 30 s and when playback stops. The resume rules are in
  [Home, history and recommendations](home.md).
- A session tracks all its runs, including those a seek stopped, and closing it waits for them
  (5 s at most) before deleting its segments.
- An administrator can see and stop current playbacks.

## A cap on concurrent video transcodes

On a NAS, three videos transcoded at once saturate the CPU and everyone stutters.

- The `max_transcodes` setting changes at runtime. At 0 it is automatic: 4 with a hardware
  encoder, otherwise one per four cores (at least one).
- Only **video** transcodes count. Direct play, remux, audio-only transcoding and music
  conversion are cheap and always allowed.
- Only **active** ones count: a run in progress, or a segment requested less than a minute ago.
  Counting open sessions would be a trap, because a paused or abandoned playback keeps its
  session for up to 30 minutes; with a cap of 1, one person could not move on to the next
  episode.
- The check happens **only when a playback opens** (`RESOURCE_EXHAUSTED`,
  `playback.transcode_limit`). A playback in progress is never cut.

## Scrubbing thumbnails

When the user drags the seek bar, the client shows a picture of the target ("trickplay").
Decoding a whole video to take a frame every 10 s costs almost as much as a transcode, so:

- **Only keyframes are decoded** (`-skip_frame nokey`), scaled to 320 px wide and written as
  JPEG. Each frame is named rank × 10⁹ + time in milliseconds: the rank keeps the timestamp
  increasing, because the encoder refuses one that goes backwards, and Go recovers the time by
  removing the rank.
- Go then fills the grid: thumbnail *n* shows time *n* × 10 s and takes the keyframe nearest to
  it. The `fps` filter is not used; fed keyframes only, it lost frames on open-GOP HEVC and left
  the end of the file without thumbnails.
- **Sheets** of 10 × 10 thumbnails (JPEG quality 80), 16 min 40 s of video each. Thumbnail *n* is
  in sheet *n* / 100, column (*n* mod 100) mod 10, row (*n* mod 100) / 10.
- HDR sources are tone mapped with `zscale` on the CPU when it is available. The thumbnails are
  small, so the GPU would bring nothing.
- One worker, after the probe of each file; again when a file's content changes.
- Sheets live in the **metadata** directory, not the cache, because they are slow to remake. Each
  generation draws a random key, and sheets are served at `GET /trickplay/{file}/{key}/{n}.jpg`
  without authentication and with an immutable cache. A daily purge removes replaced generations.
- `MediaFile.trickplay` gives the interval, thumbnail size, grid and sheet URLs. It is absent
  until generation is done, and only returned when the file's fingerprint matches: never pictures
  of other content.

A thumbnail can be off by half the distance between two keyframes (often 1 to 5 s). That does
not matter for a preview, and the seek itself stays exact. Generation takes around 10 s per
episode and 30 to 40 s for a two-hour film. The `trickplay` setting turns generation off without
deleting what exists.

## Offline downloads

A device can take a movie, a season or an album along. The original is not always playable on
the device, is often too big for a phone, and what was watched offline has to come back to the
server without overwriting what the profile did elsewhere in the meantime.

| Quality | Video at most | Video bitrate | Audio | Music |
|---|---|---|---|---|
| `ORIGINAL` | 1080p, only if it must be transcoded | — | playback settings | as is, else AAC |
| `HIGH` | 1080p | 8 Mbit/s | 192 kbit/s | AAC 256 kbit/s |
| `MEDIUM` | 720p | 4 Mbit/s | 160 kbit/s | AAC 160 kbit/s |
| `LOW` | 480p | 1.5 Mbit/s | 128 kbit/s | AAC 96 kbit/s |

- `playback.DecideDownload` (pure logic): a file the device can play and that fits the quality is
  delivered **as is**, ready at once. Otherwise a **whole MP4** (`+faststart`) is prepared, in
  which each stream is copied if possible and transcoded if not. The expected size is given
  before preparation.
- Preparation is a background job with one worker for videos; music conversions have their own
  class so an album never waits behind a film. It uses the same encoders as playback, reports
  progress from FFmpeg's `-progress` output every 2 s at most, and stops if the download is
  removed. B-frames are allowed here, since nothing is cut into segments.
- A download belongs to the device (session) and profile that asked for it, and disappears with
  the session.
- Delivery is `GET /downloads/{id}/file` with the device's bearer token and Range requests, plus
  subtitles in the formats the device displays.
- A ready download is forgotten on the server after seven days; the device keeps its file.
- **Coming back online** (`SyncOfflinePlayback`): a playback finished offline always counts,
  without erasing a more recent resume position made elsewhere. A resume position only replaces
  what is older than it. Each applied report is recorded, so a replayed report does not count
  twice.
- An administrator can deny downloads to an account, never to an administrator.

Not done yet: burning in an image subtitle for a download, multi-part movies (only the first part
is downloaded), a space quota for prepared copies.

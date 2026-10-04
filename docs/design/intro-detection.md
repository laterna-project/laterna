# Intro and credits detection

"Skip intro" and "next episode" at the credits are expected from any player. Laterna finds these
segments itself, from two sources:

- **named chapters**: fansub releases and many Blu-rays name their chapters "OP", "Opening",
  "ED", "Ending", "Preview". This is exact and free;
- **sound**: the episodes of a season share the same theme, with the same sound, at a time that
  varies with the cold open.

## Data

A segment (`media_segments`) is an intro, credits, a recap or a preview. A file has at most one
of each kind, with a start, an end and a source (chapter or audio). A chapter always wins over
audio.

Segments are exposed by `MediaFile.segments` and `StartPlaybackResponse.segments`. The client
decides what to do with them: a button, an automatic skip, "next episode".

## Chapters

`internal/segments` is pure logic. Titles are lowercased and stripped of accents, and recognized
when they start with a known name followed by nothing, a space or a digit ("OP1", "Opening 2",
but not "Edition spéciale"). From the most specific to the most general:

- credits: "end credits", "ending", "credits", "outro", "ED", "générique de fin";
- intro: "opening", "intro", "OP", "générique de début";
- recap: "previously", "recap", "dans les épisodes précédents";
- preview: "next episode", "preview", "prochain épisode", "aperçu".

"Générique" alone is the intro in the first half of the file and the credits after that.
Chapters are read during the probe, so their segments exist as soon as the file is imported.

## Audio

### Where to look

- Intro: the first third of the episode, 10 minutes at most.
- Credits: the last quarter, 8 minutes at most.

FFmpeg decodes those windows to mono 16-bit at 11,025 Hz. The track is the one in the episode's
language, otherwise the default track.

### Fingerprint

Computed in Go while decoding (`internal/media/fingerprint`):

- a frame of 0.37 s every 0.124 s;
- Fourier transform, then chroma (energy of the 12 pitch classes) smoothed over 5 frames;
- a 32-bit code per frame that compares pitch classes with each other;
- frames that are too quiet count as silence and match nothing.

### Comparison

Every offset between two fingerprints is tried. Two frames match when their codes differ by at
most 5 bits out of 32. The longest matching stretch wins, provided that:

- at least 75% of its frames match;
- it has no gap longer than 0.75 s.

On synthetic signals, the same passage encoded twice differs by 0 to 5 bits, almost always 0 or
1. Two different pieces of music in the same key differ by 11 bits on average, and by 5 or fewer
for 8% of frames. Without the density rule, those chance matches chained into false intros.

Accepted lengths: an intro lasts 15 s to 3 minutes, credits 15 s to 10 minutes. Anything shorter
is more often a jingle.

### Neighbors

Each episode is compared with the next one, the previous one, then further away, four at most.

- It keeps the longest stretch found. A neighbor whose theme changed (a new song) only shares
  the beginning.
- The result also serves neighbors that do not have this segment yet, since the episode
  processed before them had nobody to compare with. An episode always rewrites its own segments;
  it only fills in a neighbor's when they are missing.

### Job, cache, cost

The `file.segments` job has its own class and handles one file at a time. It runs after the
probe of each video, and again when the scan finds a file that was never processed, whose content
changed, or that has not been through audio yet. Audio detection only applies to episodes, and
the `detect_segments` setting turns it off.

Fingerprints are kept in the cache directory, named after the file's content, so an added episode
only computes its own. Forgotten ones are purged daily.

Cost: 0.3 s of computation for 10 minutes of sound and 65 ms per comparison, plus decoding. Under
a second per episode on a hard disk.

## Why not Chromaprint

- `libchromaprint` needs cgo, or an FFmpeg built with it; the Docker image uses a stock FFmpeg.
- Its classifiers are data under the LGPL.
- The need is simpler than recognizing a song among millions: find the same sound, from the same
  production, in two files.

## Results and limits

- On a 50-episode season of 24-minute episodes with no chapters: 47 intros of 89 s found, at
  times ranging from 1 s to 4 minutes into the episode, and 46 credits. The last episode has
  neither. On a long-running series, detection follows a change of opening in the middle of a
  season.
- Boundaries are accurate to about a quarter of a second (frame size, smoothing).
- A theme whose music changes from one episode to the next is found only if a neighbor has the
  same one. Chapters remain the reliable source.
- Movies only get segments from chapters: they have no neighbor to compare with.
- Background music repeated identically at the same place in two episodes can pass for a theme.
  The windows and the accepted lengths limit this, and a chapter overrides it.

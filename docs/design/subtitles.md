# Subtitles

The goal is that subtitles appear **instantly**, including when the viewer switches track. The
usual approach, extracting at playback time and burning in, makes people wait on large files and
restarts the transcode at every change. Laterna does the work at import instead, and lets the
client draw.

## Everything is extracted at import

The `file.subtitles` job runs after the probe of each video file, in its own class with one
worker.

- All tracks are extracted in **one FFmpeg pass**. Subtitles are interleaved with the picture, so
  the whole file has to be read anyway. If the pass fails, tracks are retried one by one to save
  the others.
- Times stay those of the source (`-copyts`), like HLS playback: a subtitle shows at the same
  instant in direct play, remux or transcode.
- Nothing is redone as long as neither the file's fingerprint nor the signature of its external
  subtitles (names, sizes, dates) changes.
- The extraction is written to a temporary folder that replaces the old one: there is never a
  half-written extraction.

Extraction reads at disk speed: a couple of seconds for a typical episode, around twenty for a
4 GB film. The first pass over a large library takes a while, once.

**A file that is not extracted yet** (just added): opening playback moves its extraction to the
front of the queue. The expected list is returned with `subtitles_ready` false, and the subtitle
URLs wait for the extraction. `PlaybackService.GetSubtitles` with `wait` then returns the final
list and the fonts.

## Formats served

- **ASS**: the original, with its styles and fonts, for libass or
  [JASSUB](https://github.com/ThaUnknown/jassub).
- **WebVTT from ASS**, for clients with no ASS renderer. It keeps dialogue only, because a fansub
  converted as is would cover the screen with signs and karaoke effects. Positioned or animated
  text (`\pos`, `\move`, `\clip`, `\org`) and drawings (`\p`) are dropped. Italic, bold, underline
  and top-of-screen placement are kept, and split lines are joined.
- **WebVTT** for other text subtitles, converted by FFmpeg.
- **`.sup`** for PGS, as is.
- **Matroska (`.mks`)** for VobSub and DVB, which are only used for burn-in.

## External subtitles

`naming.SidecarFor` recognizes these layouts: same folder, with the video's name followed by a
dot (`Movie.fr.forced.srt`); a `Subs` folder for a video alone in its folder; `Subs/<video
name>/` for series.

- The language is read from ISO codes and from English or French names; forced, hearing-impaired
  and default flags as well.
- Encoding is brought to UTF-8: BOM and UTF-16 are recognized, otherwise Windows-1252, the
  encoding of old `.srt` files (FFmpeg would read them as UTF-8 and lose the accents).
- The scan notices added or changed subtitles through their signature.

## Fonts

- Matroska attachments are read straight from the EBML structure, without walking the file: a
  few milliseconds for dozens of fonts.
- A font is recognized by its type or extension, since attachments are often declared "unknown".
- Fonts are stored by content hash: a font shared by a whole series is written once.
- Names are read from the font's `name` table, in every language, because a Japanese fansub
  refers to its fonts by their Japanese names. The table is read by our own code;
  `x/image/font/sfnt` would pull in `golang.org/x/text`.
- Fonts are served at `GET /fonts/{hash}{ext}`, without authentication and immutable, so they
  stay cached from one episode to the next.

## Serving

Subtitles are at `GET /playback/{session}/{secret}/subtitles/{position}.{format}`.
`StartPlayback` returns the tracks, their available formats, the fonts and the burned-in subtitle
if there is one. The `DeviceProfile` declares which formats the client displays itself
(`subtitle_formats`).

Switching subtitle only concerns the client: nothing is restarted on the server. In a browser,
switching takes a millisecond or two, in ASS as in WebVTT.

## Burn-in, as a last resort

A chosen subtitle that the device cannot display is burned in, and the video is then transcoded
(`playback.Decide`, `Plan.Burn`).

- **Image subtitles**: the extracted file is read as a second input, from its beginning. A line
  that started before the run's start is therefore shown; with `-ss` on the track inside the
  source file it would be skipped.
- A cropped film (1920 × 802) under Blu-ray PGS (1920 × 1080): the black bars are put back,
  because subtitles often sit in them. Subtitles smaller than the picture are scaled up.
- **Text subtitles**: libass (the `subtitles` filter). The file and its fonts are copied into the
  playback's working folder and referred to by relative paths, which an FFmpeg filter reads
  without the escaping a Windows path would need.

## Purge

`subtitles.purge` runs in the same class as extraction, so never at the same time: fonts no
longer used, folders of forgotten files, leftovers of an interrupted extraction. Daily, and after
a scan that forgot files.

## Not done

- WebVTT renditions inside the HLS playlist for native players (AVPlayer).
- OCR of image subtitles (not planned).
- Picking a subtitle automatically from the profile's languages.

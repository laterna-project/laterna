# FFmpeg in this bundle

This archive or package includes `ffmpeg` and `ffprobe`, unmodified, from the static GPL builds
published at https://github.com/BtbN/FFmpeg-Builds. The exact release and file are recorded in
[`packaging/ffmpeg.lock`](https://github.com/laterna-project/laterna/blob/main/packaging/ffmpeg.lock)
of the Laterna version you downloaded.

FFmpeg is licensed under the GNU General Public License version 3 in this configuration. Its
source code is at https://ffmpeg.org/ and https://git.ffmpeg.org/ffmpeg.git; the build scripts,
the list of libraries compiled in and their sources are in the BtbN repository at the pinned
release.

Laterna runs FFmpeg as a separate process and does not link against it. To use another FFmpeg,
delete these two files, or point `ffmpeg.ffmpeg` and `ffmpeg.ffprobe` (or `LATERNA_FFMPEG` and
`LATERNA_FFPROBE`) at it.

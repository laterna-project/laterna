package testfixtures

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Root returns the folder of the fixture library: testdata/library at the root of the repository,
// found by walking up from the current folder to go.mod (a test runs in its package's folder).
// runtime.Caller will not do: with -trimpath it gives a relative path.
func Root() string {
	dir, err := os.Getwd()
	if err != nil {
		return filepath.Join("testdata", "library")
	}
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return filepath.Join(d, "testdata", "library")
		}
		if filepath.Dir(d) == d {
			return filepath.Join(dir, "testdata", "library")
		}
	}
}

// FFmpeg returns the paths of ffmpeg and ffprobe (LATERNA_FFMPEG / LATERNA_FFPROBE, otherwise
// PATH), or ok=false if they cannot be found.
func FFmpeg() (ffmpeg, ffprobe string, ok bool) {
	find := func(env, name string) string {
		if v := os.Getenv(env); v != "" {
			return v
		}
		p, err := exec.LookPath(name)
		if err != nil {
			return ""
		}
		return p
	}
	ffmpeg, ffprobe = find("LATERNA_FFMPEG", "ffmpeg"), find("LATERNA_FFPROBE", "ffprobe")
	return ffmpeg, ffprobe, ffmpeg != "" && ffprobe != ""
}

// Library returns the fixture folder, generating the fixtures if needed. The test is skipped if
// FFmpeg is not available.
func Library(t testing.TB) string {
	t.Helper()
	ffmpeg, _, ok := FFmpeg()
	if !ok {
		t.Skip("FFmpeg not found: media test skipped (install FFmpeg or set LATERNA_FFMPEG)")
	}
	root := Root()
	if v, err := os.ReadFile(filepath.Join(root, markerFile)); err == nil && strings.TrimSpace(string(v)) == Version {
		return root
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, _, err := Generate(ctx, root, Options{FFmpeg: ffmpeg}); err != nil {
		t.Fatalf("generating fixtures: %v", err)
	}
	return root
}

// Path returns the path of a fixture, relative to the library (with forward slashes).
func Path(t testing.TB, rel string) string {
	t.Helper()
	return filepath.Join(Library(t), filepath.FromSlash(rel))
}

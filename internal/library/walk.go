// Package library walks library folders and fingerprints files. It knows nothing about the database
// or the catalog: app compares what it finds with what is known.
package library

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/naming"
)

// Entry is a file (media, subtitle, metadata) found on disk.
type Entry struct {
	// Path is the absolute path.
	Path string
	// Root is the library root the file is under.
	Root string
	// Rel is the path relative to the root, with forward slashes.
	Rel     string
	Size    int64
	ModTime time.Time
}

// Walked is the result of a walk.
type Walked struct {
	Entries []Entry
	// Subtitles are the subtitle files found (to be matched with videos, see Sidecars).
	Subtitles []Entry
	// Metadata are the NFO files and images found (see MetadataSignatures).
	Metadata []Entry
	// Unavailable lists the roots that could not be reached (share offline, disk unplugged).
	// Nothing they held may be treated as gone.
	Unavailable []string
	// Unreadable lists the subfolders that could not be read (permissions). Same caution for what
	// they hold.
	Unreadable []string
	// Counts is the number of files found per root. An empty root where files were known points to
	// a missing mount.
	Counts map[string]int
}

// Walk walks the roots of a library and returns the files to index: those media accepts
// (naming.IsVideo, naming.IsAudio for music, naming.IsBook, naming.IsPhoto), plus subtitles and
// metadata. Media wins: an image is a photo in a photo library and artwork anywhere else.
func Walk(ctx context.Context, roots []string, media func(rel string) bool) (Walked, error) {
	w := Walked{Counts: map[string]int{}}
	for _, root := range roots {
		if err := checkRoot(root); err != nil {
			w.Unavailable = append(w.Unavailable, root)
			continue
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if err != nil {
				if path == root {
					return err
				}
				w.Unreadable = append(w.Unreadable, path)
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if path != root && naming.Ignored(rel+"/x") {
					return filepath.SkipDir
				}
				return nil
			}
			isMedia, sub, meta := media(rel), naming.IsSubtitle(rel), naming.IsMetadata(rel)
			if !d.Type().IsRegular() || (!isMedia && !sub && !meta) || naming.Ignored(rel) {
				return nil
			}
			info, infoErr := d.Info()
			if infoErr != nil {
				w.Unreadable = append(w.Unreadable, path)
				return nil //nolint:nilerr // the file is recorded as unreadable and the walk goes on
			}
			e := Entry{Path: path, Root: root, Rel: rel, Size: info.Size(), ModTime: info.ModTime().UTC()}
			switch {
			case isMedia:
			case sub:
				w.Subtitles = append(w.Subtitles, e)
				return nil
			case meta:
				w.Metadata = append(w.Metadata, e)
				return nil
			}
			w.Entries = append(w.Entries, e)
			w.Counts[root]++
			return nil
		})
		if err != nil {
			if ctx.Err() != nil {
				return Walked{}, ctx.Err()
			}
			w.Unavailable = append(w.Unavailable, root)
		}
	}
	return w, nil
}

// checkRoot checks that a root exists, is a folder and can be read.
func checkRoot(root string) error {
	st, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a folder", root)
	}
	f, err := os.Open(root)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.Readdirnames(1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// Under reports whether path is inside one of the given folders, or is one of them.
func Under(path string, dirs []string) bool {
	for _, d := range dirs {
		if path == d || strings.HasPrefix(path, strings.TrimRight(d, `/\`)+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// fingerprintChunk is how much is read at the start and at the end of the file.
const fingerprintChunk = 64 << 10

// Fingerprint hashes a file: its size, its first 64 KiB and its last 64 KiB (the whole file if it
// is smaller). That is enough to recognize a renamed file while reading only 128 KiB, even over a
// network share.
func Fingerprint(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := st.Size()
	h := sha256.New()
	var sizeBytes [8]byte
	binary.LittleEndian.PutUint64(sizeBytes[:], uint64(size)) //nolint:gosec // a file size is never negative
	h.Write(sizeBytes[:])
	if size <= 2*fingerprintChunk {
		if _, err := io.Copy(h, f); err != nil {
			return "", err
		}
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	if _, err := io.CopyN(h, f, fingerprintChunk); err != nil {
		return "", err
	}
	if _, err := f.Seek(size-fingerprintChunk, io.SeekStart); err != nil {
		return "", err
	}
	if _, err := io.CopyN(h, f, fingerprintChunk); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// MetadataSignatures sums up, for each folder that has any, its metadata files (names, sizes,
// dates): adding, changing or removing an NFO or an image changes the signature of its folder. The
// key is the folder's absolute path.
func MetadataSignatures(w Walked) map[string]string {
	byDir := map[string][]Entry{}
	for _, e := range w.Metadata {
		dir := filepath.Dir(e.Path)
		byDir[dir] = append(byDir[dir], e)
	}
	out := make(map[string]string, len(byDir))
	for dir, entries := range byDir {
		slices.SortFunc(entries, func(a, b Entry) int { return strings.Compare(a.Path, b.Path) })
		h := sha256.New()
		for _, e := range entries {
			_, _ = fmt.Fprintf(h, "%s\x00%d\x00%d\n", filepath.Base(e.Path), e.Size, e.ModTime.UnixMilli())
		}
		out[dir] = hex.EncodeToString(h.Sum(nil))[:32]
	}
	return out
}

package library

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"

	"github.com/laterna-project/laterna/internal/naming"
)

// Folder picker: what the server sees of its disks, so that library folders can be chosen from a
// client that cannot see them. Only folder names are given out, never file contents.

const (
	// maxFolders is the most subfolders returned for one folder (in natural order).
	maxFolders = 1000
	// probeEntries is how many entries are read in each subfolder to guess what it holds.
	probeEntries = 200
	// probeWorkers is how many subfolders are probed at once (a network share is slow to answer).
	probeWorkers = 8
)

// MediaCounts counts media files by kind.
type MediaCounts struct {
	Videos, Audio, Books, Photos int
}

func (m *MediaCounts) add(name string) {
	switch {
	case naming.Ignored(name), naming.IsArtwork(name):
	case naming.IsVideo(name):
		m.Videos++
	case naming.IsAudio(name):
		m.Audio++
	case naming.IsBook(name):
		m.Books++
	case naming.IsPhoto(name):
		m.Photos++
	}
}

// settle drops the images of a video or music folder: those are covers or thumbs, not photos.
func (m *MediaCounts) settle() {
	if m.Videos > 0 || m.Audio > 0 {
		m.Photos = 0
	}
}

// Folder is a folder offered by the picker.
type Folder struct {
	Name, Path string
	// Readable means the server can open it.
	Readable bool
	// HasSubfolders means it has at least one folder among its first entries.
	HasSubfolders bool
	// Media counts media files among its first entries. A hint, not a total.
	Media MediaCounts
}

// Listing is the content of a folder: its subfolders and its own media files.
type Listing struct {
	Path string
	// Parent is the folder above; "" at the root of a disk, where going up leads back to the
	// starting points.
	Parent  string
	Folders []Folder
	Media   MediaCounts
	// Truncated means there are more than maxFolders subfolders and only the first ones are listed.
	Truncated bool
}

// Starts returns the picker's starting points: the drives on Windows; elsewhere the root and the
// folders where disks and container volumes are usually mounted.
func Starts(ctx context.Context) []Folder {
	var paths []string
	if runtime.GOOS == "windows" {
		for c := 'A'; c <= 'Z'; c++ {
			paths = append(paths, string(c)+`:\`)
		}
	} else {
		paths = []string{"/", "/media", "/mnt", "/srv", "/data", "/volume1", "/share"}
	}
	folders := make([]Folder, len(paths))
	for i, p := range paths {
		folders[i] = Folder{Name: p, Path: p}
	}
	probeAll(ctx, folders)
	var out []Folder
	for _, f := range folders {
		if f.Readable || runtime.GOOS != "windows" && f.Path == "/" {
			out = append(out, f)
		}
	}
	return out
}

// List returns the subfolders of a folder in natural order, without hidden or system folders and
// without those in hide. Each one is probed: readable or not, with subfolders or not, a few media
// files.
func List(ctx context.Context, dir string, hide []string) (Listing, error) {
	dir = filepath.Clean(dir)
	if !filepath.IsAbs(dir) {
		return Listing{}, errors.New("absolute path expected")
	}
	f, err := os.Open(dir)
	if err != nil {
		return Listing{}, err
	}
	defer func() { _ = f.Close() }()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return Listing{}, err
	}
	l := Listing{Path: dir}
	if parent := filepath.Dir(dir); parent != dir {
		l.Parent = parent
	}
	for _, e := range entries {
		name := e.Name()
		if !isDir(dir, e) {
			l.Media.add(name)
			continue
		}
		p := filepath.Join(dir, name)
		if naming.SystemDir(name) || slices.Contains(hide, p) {
			continue
		}
		l.Folders = append(l.Folders, Folder{Name: name, Path: p})
	}
	l.Media.settle()
	slices.SortStableFunc(l.Folders, func(a, b Folder) int { return naming.NaturalCompare(a.Name, b.Name) })
	if len(l.Folders) > maxFolders {
		l.Folders, l.Truncated = l.Folders[:maxFolders], true
	}
	probeAll(ctx, l.Folders)
	return l, ctx.Err()
}

// isDir reports whether an entry is a folder, following a symlink.
func isDir(dir string, e fs.DirEntry) bool {
	if e.IsDir() {
		return true
	}
	if e.Type()&fs.ModeSymlink == 0 {
		return false
	}
	st, err := os.Stat(filepath.Join(dir, e.Name()))
	if err != nil {
		return false
	}
	return st.IsDir()
}

// probeAll probes folders, a few at a time.
func probeAll(ctx context.Context, folders []Folder) {
	sem := make(chan struct{}, probeWorkers)
	var wg sync.WaitGroup
	for i := range folders {
		if ctx.Err() != nil {
			break
		}
		f := &folders[i]
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			probe(f.Path, f)
		})
	}
	wg.Wait()
}

// probe reads the first entries of a folder.
func probe(dir string, f *Folder) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer func() { _ = d.Close() }()
	entries, err := d.ReadDir(probeEntries)
	if err != nil && !errors.Is(err, io.EOF) {
		return
	}
	f.Readable = true
	for _, e := range entries {
		switch {
		case !isDir(dir, e):
			f.Media.add(e.Name())
		case !naming.SystemDir(e.Name()):
			f.HasSubfolders = true
		}
	}
	f.Media.settle()
}

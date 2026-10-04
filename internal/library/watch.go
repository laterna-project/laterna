package library

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"

	"github.com/laterna-project/laterna/internal/naming"
)

// Folder watching: a change under a root (file added, changed, removed or renamed, folder created)
// is reported right away. Nothing is decided here. An event is only a hint and the scan is the
// source of truth; a lost event is caught by the periodic scan.

// watchBuffer is the buffer of each watched folder on Windows (64 KiB by default, one per folder).
// An overflow loses nothing useful: it reports the root like anything else.
const watchBuffer = 16 << 10

// Watcher watches the folders of library roots. fsnotify does not follow subfolders, so each one is
// watched separately, and new ones as soon as they are created.
type Watcher struct {
	fs      *fsnotify.Watcher
	changes chan string
	onError func(error)

	mu    sync.Mutex
	roots map[string]bool
	// dirs maps a watched folder to its root.
	dirs map[string]string
}

// NewWatcher opens a watcher. onError receives watch errors, for the log.
func NewWatcher(onError func(error)) (*Watcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &Watcher{fs: w, changes: make(chan string, 256), onError: onError, roots: map[string]bool{}, dirs: map[string]string{}}, nil
}

// Changes returns the channel of roots under which something changed.
func (w *Watcher) Changes() <-chan string { return w.changes }

// Close stops watching.
func (w *Watcher) Close() error { return w.fs.Close() }

// Dirs returns the number of watched folders.
func (w *Watcher) Dirs() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.dirs)
}

// Sync watches exactly these roots: it adds the new ones (with all their subfolders) and forgets
// the others. It returns the roots that could not be watched (unreachable, or too many folders for
// the OS) with the first error; those are to be retried.
func (w *Watcher) Sync(roots []string) (failed []string, err error) {
	want := map[string]bool{}
	for _, r := range roots {
		want[filepath.Clean(r)] = true
	}
	w.mu.Lock()
	var gone []string
	for r := range w.roots {
		if !want[r] {
			gone = append(gone, r)
		}
	}
	w.mu.Unlock()
	for _, r := range gone {
		w.forget(r, "")
	}
	for r := range want {
		w.mu.Lock()
		known := w.roots[r]
		w.mu.Unlock()
		if known {
			continue
		}
		if addErr := w.addTree(r, r); addErr != nil {
			w.forget(r, "")
			failed = append(failed, r)
			if err == nil {
				err = addErr
			}
			continue
		}
		w.mu.Lock()
		w.roots[r] = true
		w.mu.Unlock()
	}
	return failed, err
}

// addTree watches dir and its subfolders (except ignored ones) under root.
func (w *Watcher) addTree(root, dir string) error {
	if err := checkRoot(dir); err != nil {
		return err
	}
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dir {
				return err
			}
			return nil // unreadable folder: the scan reports it, watching goes on
		}
		if !d.IsDir() {
			return nil
		}
		if p != root && ignored(root, p) {
			return filepath.SkipDir
		}
		if err := w.fs.AddWith(p, fsnotify.WithBufferSize(watchBuffer)); err != nil {
			return err
		}
		w.mu.Lock()
		w.dirs[p] = root
		w.mu.Unlock()
		return nil
	})
}

// forget stops watching a root (under empty) or a folder and its subfolders.
func (w *Watcher) forget(root, under string) {
	w.mu.Lock()
	var drop []string
	for d, r := range w.dirs {
		if (under == "" && r == root) || (under != "" && (d == under || strings.HasPrefix(d, under+string(filepath.Separator)))) {
			drop = append(drop, d)
			delete(w.dirs, d)
		}
	}
	if under == "" {
		delete(w.roots, root)
	}
	w.mu.Unlock()
	for _, d := range drop {
		_ = w.fs.Remove(d) // already removed by the OS if the folder is gone
	}
}

// ignored reports the files and folders the scan ignores (hidden, extras, NAS folders).
func ignored(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." {
		return false
	}
	return naming.Ignored(filepath.ToSlash(rel) + "/x")
}

// rootOf returns the root of a touched path: that of its folder, or its own if it is a watched
// folder.
func (w *Watcher) rootOf(p string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if r, ok := w.dirs[p]; ok {
		return r
	}
	return w.dirs[filepath.Dir(p)]
}

// Run reads events until ctx is canceled and reports the root of each one.
func (w *Watcher) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.fs.Events:
			if !ok {
				return
			}
			w.handle(ctx, ev)
		case err, ok := <-w.fs.Errors:
			if !ok {
				return
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				// Events were lost: every root needs another look.
				w.mu.Lock()
				roots := make([]string, 0, len(w.roots))
				for r := range w.roots {
					roots = append(roots, r)
				}
				w.mu.Unlock()
				for _, r := range roots {
					w.signal(ctx, r)
				}
				continue
			}
			if w.onError != nil {
				w.onError(err)
			}
		}
	}
}

func (w *Watcher) handle(ctx context.Context, ev fsnotify.Event) {
	root := w.rootOf(ev.Name)
	if root == "" {
		return
	}
	rel, err := filepath.Rel(root, ev.Name)
	if err == nil && rel != "." && naming.Ignored(filepath.ToSlash(rel)) {
		return // hidden file, NAS folder...
	}
	if ev.Op == fsnotify.Write {
		w.mu.Lock()
		_, isDir := w.dirs[ev.Name]
		w.mu.Unlock()
		if isDir {
			return // Windows: the folder of a touched file, already reported by the file itself
		}
	}
	switch {
	case ev.Has(fsnotify.Create):
		// New folder (created or moved here): watch it too, with its content.
		if st, err := os.Stat(ev.Name); err == nil && st.IsDir() && !ignored(root, ev.Name) {
			if err := w.addTree(root, ev.Name); err != nil && w.onError != nil {
				w.onError(err)
			}
		}
	case ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename):
		if filepath.Clean(ev.Name) == root {
			w.forget(root, "") // the root is gone: watch it again when it comes back
		} else {
			w.forget(root, ev.Name)
		}
	}
	w.signal(ctx, root)
}

func (w *Watcher) signal(ctx context.Context, root string) {
	select {
	case w.changes <- root:
	case <-ctx.Done():
	}
}

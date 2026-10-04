package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/jobs"
	"github.com/laterna-project/laterna/internal/metadata"
	"github.com/laterna-project/laterna/internal/metadata/download"
	"github.com/laterna-project/laterna/internal/store"
)

// Item images and cast photos. An image sitting next to the media is read in place. An image that
// only an NFO gives a URL for is downloaded into MetadataDir under a name derived from the URL, so
// a new URL gives a new file. Files that are no longer needed are deleted by the daily purge.

// wantedImage is an image an item should have.
type wantedImage struct {
	kind   domain.ImageKind
	source domain.ImageSource
	path   string
	url    string // downloaded image: its URL
}

// wantedImages picks the images of an item: those next to the media, then those extracted from its
// files (album cover), then, for the kinds that still have none, those the NFO gives a URL for (if
// downloads are allowed).
func (a *App) wantedImages(itemID domain.ID, local []metadata.Artwork, embedded []wantedImage, nfo *metadata.NFO) []wantedImage {
	var out []wantedImage
	have := map[domain.ImageKind]bool{}
	for _, aw := range local {
		have[aw.Kind] = true
		out = append(out, wantedImage{kind: aw.Kind, source: domain.ImageLocal, path: aw.Path})
	}
	for _, w := range embedded {
		if !have[w.kind] {
			have[w.kind] = true
			out = append(out, w)
		}
	}
	if nfo == nil || !a.Settings().DownloadImages {
		return out
	}
	for _, kind := range domain.ImageKinds {
		if u := nfo.Images[kind]; u != "" && !have[kind] {
			out = append(out, wantedImage{kind: kind, source: domain.ImageRemote, path: a.remotePath("images", itemID, string(kind), u), url: u})
		}
	}
	return out
}

// remotePath is where a downloaded image goes: <MetadataDir>/<dir>/<first 2
// chars>/<id>/<name>-<hash of the URL><extension of the URL>.
func (a *App) remotePath(dir string, owner domain.ID, name, u string) string {
	sum := sha256.Sum256([]byte(u))
	ext := strings.ToLower(path.Ext(strings.SplitN(u, "?", 2)[0]))
	if _, ok := imageTypes[ext]; !ok {
		ext = "" // the type is read from the file when serving it
	}
	id := owner.String()
	return filepath.Join(a.metadataDir, dir, id[:2], id, name+"-"+hex.EncodeToString(sum[:4])+ext)
}

// syncImages brings the images of an item in line with the wanted ones: new or changed ones are
// analyzed (local) or downloaded, and those no longer wanted are removed.
func (a *App) syncImages(ctx context.Context, q store.Q, itemID domain.ID, want []wantedImage) error {
	now := a.now()
	wanted := map[domain.ImageKind]bool{}
	for _, w := range want {
		wanted[w.kind] = true
		img, changed, err := q.SetItemImage(ctx, itemID, w.kind, w.source, w.path, w.url, now)
		if err != nil {
			return err
		}
		if changed {
			if err := a.enqueueImage(ctx, q, img); err != nil {
				return err
			}
		}
	}
	existing, err := q.ItemImages(ctx, itemID)
	if err != nil {
		return err
	}
	for _, img := range existing {
		if !wanted[img.Kind] {
			if err := q.DeleteImage(ctx, img.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// setPeopleImages gives a photo to each credited person the NFO gives a URL for. A person keeps the
// first photo obtained: Sonarr and Radarr NFO files give different URLs for the same person, who
// would otherwise change face every time metadata is read.
func (a *App) setPeopleImages(ctx context.Context, q store.Q, credits []domain.Credit, people []domain.ID) error {
	if !a.Settings().DownloadImages {
		return nil
	}
	now := a.now()
	for i, c := range credits {
		if c.Thumb == "" {
			continue
		}
		cur, err := q.PersonImage(ctx, people[i])
		if err == nil && cur.Hash != "" {
			if _, statErr := os.Stat(cur.Path); statErr == nil {
				continue
			}
		} else if err != nil && !store.IsNotFound(err) {
			return err
		}
		img, changed, err := q.SetPersonImage(ctx, people[i], a.remotePath("people", people[i], "photo", c.Thumb), c.Thumb, now)
		if err != nil {
			return err
		}
		if changed {
			if err := a.enqueueImage(ctx, q, img); err != nil {
				return err
			}
		}
	}
	return nil
}

// enqueueImage asks for the analysis of a local image, or the download of a remote one.
func (a *App) enqueueImage(ctx context.Context, q store.Q, img domain.Image) error {
	kind := jobAnalyzeImage
	if img.Source == domain.ImageRemote {
		kind = jobDownloadImage
	}
	return a.jobs.Enqueue(ctx, q, kind, img.ID.String(), priorityBackground)
}

// downloadImage downloads an image from the URL an NFO gives, then has it analyzed. An unavailable
// image (not found, refused) is removed: another URL can be tried, or the same one the next time
// the NFO changes.
func (a *App) downloadImage(ctx context.Context, target string) error {
	id, err := domain.ParseID(target)
	if err != nil {
		return jobs.Permanent(err)
	}
	img, err := a.store.Read().Image(ctx, id)
	if store.IsNotFound(err) || (err == nil && img.Source != domain.ImageRemote) {
		return nil // replaced or removed in the meantime
	}
	if err != nil {
		return err
	}
	if !a.Settings().DownloadImages {
		return nil
	}
	if _, err := os.Stat(img.Path); errors.Is(err, fs.ErrNotExist) {
		if err := a.download.Image(ctx, img.RemoteURL, img.Path); err != nil {
			if errors.Is(err, download.ErrUnavailable) {
				// The site's or the NFO's fault, not the server's: logged, without a failed job.
				a.log.WarnContext(ctx, "image unavailable, removed", "url", img.RemoteURL, "err", err)
				return a.store.Write(ctx, func(q store.Q) error { return q.DeleteImage(ctx, img.ID) })
			}
			return err
		}
	} else if err != nil {
		return err
	}
	if err := a.store.Write(ctx, func(q store.Q) error {
		return a.jobs.Enqueue(ctx, q, jobAnalyzeImage, img.ID.String(), priorityBackground)
	}); err != nil {
		return err
	}
	a.jobs.Kick()
	return nil
}

// purgeMetadata forgets the people who are no longer credited anywhere and deletes the downloaded
// images that are no longer used (replaced, removed, or left by older versions). A recent file is
// spared: its download may just have finished.
func (a *App) purgeMetadata(ctx context.Context, _ string) error {
	var people int64
	if err := a.store.Write(ctx, func(q store.Q) error {
		var err error
		people, err = q.DeleteOrphanPeople(ctx)
		return err
	}); err != nil {
		return err
	}
	paths, err := a.store.Read().RemoteImagePaths(ctx)
	if err != nil {
		return err
	}
	// Paths relative to the metadata folder, which is walked without ever leaving it (os.Root).
	used := make(map[string]bool, len(paths))
	for _, p := range paths {
		if rel, err := filepath.Rel(a.metadataDir, p); err == nil {
			used[filepath.ToSlash(rel)] = true
		}
	}
	root, err := os.OpenRoot(a.metadataDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	cutoff := time.Now().Add(-time.Hour)
	files := 0
	for _, dir := range []string{"images", "people"} {
		var dirs []string
		err := fs.WalkDir(root.FS(), dir, func(p string, d fs.DirEntry, err error) error {
			switch {
			case errors.Is(err, fs.ErrNotExist):
				return nil
			case err != nil:
				return err
			case d.IsDir():
				dirs = append(dirs, p)
				return nil
			case used[p]:
				return nil
			}
			info, err := d.Info()
			if err != nil || info.ModTime().After(cutoff) {
				return nil //nolint:nilerr // file gone in the meantime, or too recent
			}
			if root.Remove(p) == nil {
				files++
			}
			return nil
		})
		if err != nil {
			return err
		}
		// Emptied folders, deepest first (failing has no effect if something is left in them).
		for _, d := range slices.Backward(dirs) {
			_ = root.Remove(d)
		}
	}
	sheets, err := a.purgeTrickplay(ctx, root)
	if err != nil {
		return err
	}
	if people > 0 || files > 0 || sheets > 0 {
		a.log.InfoContext(ctx, "metadata: purge", "people", people, "files", files, "trickplay", sheets)
	}
	return nil
}

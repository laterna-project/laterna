package library

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/laterna-project/laterna/internal/naming"
)

// Sidecar is an external subtitle file of a video, and what its name says about it.
type Sidecar struct {
	Entry
	naming.Sidecar
}

// Sidecars matches each video of w with its external subtitles (naming.SidecarFor). The key is the
// absolute path of the video and the subtitles are sorted by path. The .sub of a VobSub pair (the
// data of its .idx) is not a subtitle of its own.
func Sidecars(w Walked) map[string][]Sidecar {
	type key struct{ root, dir string }
	videos := map[key]int{}
	for _, e := range w.Entries {
		videos[key{e.Root, path.Dir(e.Rel)}]++
	}
	// Subtitles by folder and by parent folder, two levels up ("Subs/<video>/").
	near := map[key][]Entry{}
	for _, s := range w.Subtitles {
		d := path.Dir(s.Rel)
		for range 3 {
			near[key{s.Root, d}] = append(near[key{s.Root, d}], s)
			if d == "." {
				break
			}
			d = path.Dir(d)
		}
	}
	out := map[string][]Sidecar{}
	for _, v := range w.Entries {
		k := key{v.Root, path.Dir(v.Rel)}
		candidates := near[k]
		for _, s := range candidates {
			if d, ok := naming.SidecarFor(v.Rel, s.Rel, videos[k] == 1); ok && !vobSubData(s, candidates) {
				out[v.Path] = append(out[v.Path], Sidecar{Entry: s, Sidecar: d})
			}
		}
		slices.SortFunc(out[v.Path], func(a, b Sidecar) int { return strings.Compare(a.Rel, b.Rel) })
	}
	return out
}

// vobSubData reports a .sub that comes with an .idx of the same name.
func vobSubData(s Entry, all []Entry) bool {
	if !strings.EqualFold(path.Ext(s.Rel), ".sub") {
		return false
	}
	idx := strings.TrimSuffix(s.Rel, path.Ext(s.Rel))
	return slices.ContainsFunc(all, func(e Entry) bool {
		return strings.EqualFold(e.Rel, idx+".idx")
	})
}

// Signature sums up the external subtitles of a video (paths relative to its folder, sizes, dates):
// adding, changing or removing a file changes it. Empty when there is none.
func Signature(video Entry, subs []Sidecar) string {
	if len(subs) == 0 {
		return ""
	}
	dir := path.Dir(video.Rel) + "/"
	h := sha256.New()
	for _, s := range subs {
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00%d\n", strings.TrimPrefix(s.Rel, dir), s.Size, s.ModTime.UnixMilli())
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// Near walks the folder of a video: its videos and subtitles, and those in its subtitle folders
// ("Subs", two levels deep). That is enough to find the external subtitles of a video (Sidecars,
// Signature) without walking the whole library. Paths are relative to the folder.
func Near(dir string) (Walked, error) {
	w := Walked{Counts: map[string]int{}}
	var read func(rel string, depth int) error
	read = func(rel string, depth int) error {
		entries, err := os.ReadDir(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		for _, d := range entries {
			name := path.Join(rel, d.Name())
			if d.IsDir() {
				if (depth == 0 && naming.IsSubtitleDir(d.Name())) || depth == 1 {
					_ = read(name, depth+1) // unreadable subtitle folder: do without it
				}
				continue
			}
			video, sub := naming.IsVideo(name), naming.IsSubtitle(name)
			if !d.Type().IsRegular() || (!video && !sub) || naming.Ignored(name) || (video && depth > 0) {
				continue
			}
			info, err := d.Info()
			if err != nil {
				continue
			}
			e := Entry{Path: filepath.Join(dir, filepath.FromSlash(name)), Root: dir, Rel: name, Size: info.Size(), ModTime: info.ModTime().UTC()}
			if sub {
				w.Subtitles = append(w.Subtitles, e)
			} else {
				w.Entries = append(w.Entries, e)
			}
		}
		return nil
	}
	return w, read(".", 0)
}

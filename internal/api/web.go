package api

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

// webClient serves a built web client from dir, behind every other route. A path that names a file
// gets that file. Any other page the browser navigates to gets the client's index.html: the client
// routes its own addresses (/movies, /device...). Everything else stays a 404, so an unknown API
// call or a missing script never receives a page of HTML.
func webClient(dir string) http.Handler {
	files := os.DirFS(dir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" && name != "index.html" && isFile(files, name) {
			if strings.HasPrefix(name, "assets/") {
				// Built assets are named after their content: a name never gets other content.
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			// os.DirFS only opens valid paths inside dir, and name is cleaned above.
			http.ServeFileFS(w, r, files, name) //nolint:gosec // G703: see above
			return
		}
		if name != "" && !strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.NotFound(w, r)
			return
		}
		if !isFile(files, "index.html") {
			http.NotFound(w, r)
			return
		}
		// Checked again on every load, so that a new version of the client is picked up.
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, files, "index.html")
	})
}

func isFile(files fs.FS, name string) bool {
	info, err := fs.Stat(files, name)
	if err != nil || info == nil {
		return false
	}
	return info.Mode().IsRegular()
}

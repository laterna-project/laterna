package api

import (
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/laterna-project/laterna/internal/api/httpx"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// playback serves the bytes of a playback opened with PlaybackService.StartPlayback:
//
//	GET /playback/{session}/{secret}/direct     the file, or its audio conversion (Range)
//	GET /playback/{session}/{secret}/main.m3u8  the HLS playlist
//	GET /playback/{session}/{secret}/init.mp4   the fMP4 init segment
//	GET /playback/{session}/{secret}/{n}.m4s    a segment
//
// There is no Authorization header (<video> element): the session secret stands in for it.
func playback(a *app.App, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := domain.ParseID(r.PathValue("session"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		token, name := r.PathValue("token"), r.PathValue("file")
		ctx := r.Context()
		var path, contentType string
		switch {
		case name == "direct":
			path, err = a.DirectFile(ctx, id, token)
			contentType = mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
		case name == "main.m3u8":
			var pl string
			if pl, err = a.Playlist(id, token); err == nil {
				w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
				w.Header().Set("Cache-Control", "no-cache")
				_, _ = w.Write([]byte(pl)) //nolint:gosec // HLS playlist (mpegurl) produced by the server, not HTML
				return
			}
		case name == "init.mp4":
			path, err = a.InitSegment(ctx, id, token)
			contentType = "video/mp4"
		case strings.HasSuffix(name, ".m4s"):
			n, convErr := strconv.Atoi(strings.TrimSuffix(name, ".m4s"))
			if convErr != nil {
				http.NotFound(w, r)
				return
			}
			path, err = a.MediaSegment(ctx, id, token, n)
			contentType = "video/iso.segment"
		default:
			http.NotFound(w, r)
			return
		}
		// Segments and the init segment do not change during the session, and neither does the
		// direct file.
		serveFile(w, r, log, path, contentType, "private, max-age=3600", err)
	})
}

// subtitleTypes are the content types of the subtitles we serve, by format.
var subtitleTypes = map[string]string{
	"vtt": "text/vtt; charset=utf-8",
	"ass": "text/x-ssa; charset=utf-8",
	"sup": "application/octet-stream",
	"mks": "video/x-matroska",
}

// subtitle serves a subtitle of a playback, waiting for its extraction if it is under way:
//
//	GET /playback/{session}/{secret}/subtitles/{position}.{format}
func subtitle(a *app.App, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := domain.ParseID(r.PathValue("session"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		pos, format, ok := strings.Cut(r.PathValue("file"), ".")
		n, convErr := strconv.Atoi(pos)
		contentType, known := subtitleTypes[format]
		if !ok || convErr != nil || !known {
			http.NotFound(w, r)
			return
		}
		path, err := a.SubtitleFile(r.Context(), id, r.PathValue("token"), n, format)
		serveFile(w, r, log, path, contentType, "private, max-age=3600", err)
	})
}

// fontTypes are the content types of the fonts we serve, by extension.
var fontTypes = map[string]string{".ttf": "font/ttf", ".otf": "font/otf", ".ttc": "font/collection", ".otc": "font/collection"}

// font serves a font used by ASS subtitles, by the hash of its content: no authentication (like
// images) and immutable, so the client keeps it cached from one episode to the next.
//
//	GET /fonts/{hash}{extension}
func font(a *app.App, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("file")
		ext := strings.ToLower(filepath.Ext(name))
		f, path, err := a.FontFile(r.Context(), strings.TrimSuffix(name, filepath.Ext(name)))
		if err == nil && f.Ext != ext {
			err = domain.NotFound("subtitle.font_not_found")
		}
		serveFile(w, r, log, path, fontTypes[ext], "public, max-age=31536000, immutable", err)
	})
}

// serveFile serves a file prepared by app (stream, subtitle, font, sheet), or the error that
// prevents it: not found (404) or unavailable (503, logged). Nothing is written if the client is
// gone.
func serveFile(w http.ResponseWriter, r *http.Request, log *slog.Logger, path, contentType, cacheControl string, err error) {
	ctx := r.Context()
	var de *domain.Error
	switch {
	case errors.As(err, &de) && errors.Is(de.Kind, domain.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		if ctx.Err() == nil {
			log.ErrorContext(ctx, "cannot serve file", "path", r.URL.Path, "err", err, "request_id", httpx.RequestIDFrom(ctx))
			writeError(w, r, http.StatusServiceUnavailable, domain.T("error.server.unavailable"))
		}
		return
	}
	f, err := os.Open(path) //nolint:gosec // path taken from the database or the cache, never from the request
	if err != nil {
		log.WarnContext(ctx, "unreadable file", "path", path, "err", err)
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Cache-Control", cacheControl)
	http.ServeContent(w, r, "", st.ModTime(), f)
}

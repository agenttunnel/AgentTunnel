// Package webui serves the desktop frontend's built SPA from the daemon
// process, so an HTTP-speaking client (the `agenttunnel --web` browser mode)
// gets the same UI the Wails shell embeds. The dist/ tree is produced by the
// frontend build and copied in by desktop/build.sh; a committed placeholder
// index.html keeps `go:embed` valid on a fresh checkout.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var files embed.FS

// assetCache is the Cache-Control for content-hashed files under /assets/:
// Vite renames them when their contents change, so they may be cached
// indefinitely.
const assetCache = "public, max-age=31536000, immutable"

// Handler serves the embedded SPA. Mount it as the catch-all route; the
// daemon's own paths (/ws, /conn, ...) must be registered before it.
func Handler() http.Handler { return handler(distFS()) }

// distFS is the embedded dist/ tree. `go:embed all:dist` guarantees it
// exists, so fs.Sub cannot fail here.
func distFS() fs.FS {
	sub, err := fs.Sub(files, "dist")
	if err != nil {
		panic("webui: embedded dist missing: " + err.Error())
	}
	return sub
}

// handler serves the SPA rooted at fsys. Paths that name a real file are
// served as-is; every other path — client-side routes like /threads/xyz —
// falls back to index.html so the frontend router can pick it up. The index
// always answers 200 with no-cache; hashed assets under /assets/ get a
// long-lived cache instead.
func handler(fsys fs.FS) http.Handler {
	serveFiles := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if name := strings.TrimPrefix(r.URL.Path, "/"); name != "" {
			if info, err := fs.Stat(fsys, name); err == nil && !info.IsDir() {
				if strings.HasPrefix(r.URL.Path, "/assets/") {
					w.Header().Set("Cache-Control", assetCache)
				}
				serveFiles.ServeHTTP(w, r)
				return
			}
		}
		serveIndex(w, r, fsys)
	})
}

// serveIndex replies with index.html. no-cache lets a rebuilt SPA be picked
// up on the next reload while the hashed assets it references stay cached.
func serveIndex(w http.ResponseWriter, r *http.Request, fsys fs.FS) {
	data, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		http.Error(w, "web UI is missing its index.html", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

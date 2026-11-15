// Package webui serves the desktop frontend's built SPA from the daemon
// process, so an HTTP-speaking client (the `agenttunnel --web` browser mode)
// gets the same UI the Wails shell embeds. The dist/ tree is produced by the
// frontend build and copied in by desktop/build.sh; a committed placeholder
// index.html keeps `go:embed` valid on a fresh checkout.
package webui

import (
	"bytes"
	"embed"
	"encoding/json"
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

// PrefsFunc returns the browser preferences to seed into the page before the
// app's scripts run.
type PrefsFunc func() map[string]string

// Handler serves the embedded SPA. Mount it as the catch-all route; the
// daemon's own paths (/ws, /conn, ...) must be registered before it.
func Handler() http.Handler { return handler(distFS(), nil) }

// HandlerWithPrefs serves the SPA seeded with saved client preferences. The
// web server's origin changes on every run, so this is what lets localStorage
// survive a restart.
func HandlerWithPrefs(prefs PrefsFunc) http.Handler { return handler(distFS(), prefs) }

// HandlerFor serves an arbitrary SPA build seeded with client preferences. The
// desktop shell uses it for its embedded assets, so both clients start from
// the same saved values.
func HandlerFor(fsys fs.FS, prefs PrefsFunc) http.Handler { return handler(fsys, prefs) }

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
func handler(fsys fs.FS, prefs PrefsFunc) http.Handler {
	serveFiles := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// index.html always goes through serveIndex so it is seeded, even when
		// a client asks for the file by name instead of the root.
		if name := strings.TrimPrefix(r.URL.Path, "/"); name != "" && name != "index.html" {
			if info, err := fs.Stat(fsys, name); err == nil && !info.IsDir() {
				if strings.HasPrefix(r.URL.Path, "/assets/") {
					w.Header().Set("Cache-Control", assetCache)
				}
				serveFiles.ServeHTTP(w, r)
				return
			}
		}
		serveIndex(w, r, fsys, prefs)
	})
}

// serveIndex replies with index.html. no-cache lets a rebuilt SPA be picked
// up on the next reload while the hashed assets it references stay cached.
func serveIndex(w http.ResponseWriter, r *http.Request, fsys fs.FS, prefs PrefsFunc) {
	data, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		http.Error(w, "web UI is missing its index.html", http.StatusInternalServerError)
		return
	}
	if prefs != nil {
		data = seedPrefs(data, prefs())
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// seedPrefs puts a script in <head> that fills localStorage before the app's
// own scripts (including the pre-paint theme script) run. It goes before the
// first existing <script> so <meta charset> keeps its early position. The
// server is canonical: an origin that happens to be reused must not resurrect
// values from an older run. json.Marshal escapes <, >, and & so a value can
// never close the script tag.
func seedPrefs(index []byte, prefs map[string]string) []byte {
	if len(prefs) == 0 {
		return index
	}
	data, err := json.Marshal(prefs)
	if err != nil {
		return index
	}
	headStart := bytes.Index(index, []byte("<head"))
	if headStart < 0 {
		return index
	}
	openEnd := bytes.IndexByte(index[headStart:], '>')
	if openEnd < 0 {
		return index
	}
	headBody := headStart + openEnd + 1
	headEnd := bytes.Index(index[headBody:], []byte("</head>"))
	if headEnd < 0 {
		return index
	}
	at := headBody + headEnd
	if first := bytes.Index(index[headBody:at], []byte("<script")); first >= 0 {
		at = headBody + first
	}
	script := append([]byte(`<script>try{var p=`), data...)
	// Each key gets its own try so one value over quota cannot block the rest.
	script = append(script, []byte(`;for(var k in p){try{localStorage.setItem(k,p[k])}catch(e){}}}catch(e){}</script>`)...)
	out := make([]byte, 0, len(index)+len(script))
	out = append(out, index[:at]...)
	out = append(out, script...)
	out = append(out, index[at:]...)
	return out
}

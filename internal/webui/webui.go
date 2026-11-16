// Package webui serves the web interface that is built into the binary.
package webui

import (
	"bytes"
	"embed"
	"html"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist holds the production build of the interface (web/dist), copied here
// by "make web". Only a placeholder is kept in the repository.
//
//go:embed all:dist
var dist embed.FS

const notBuilt = `<!doctype html><meta charset="utf-8"><title>hls-monitor</title>
<body style="font-family:system-ui;background:#0a1628;color:#cad2dc;padding:3rem">
<h1>hls-monitor</h1>
<p>The web interface is not built into this binary. Run <code>make web</code> and rebuild,
or use the API: <a style="color:#00b4d8" href="swagger/index.html">swagger/</a>.</p>`

// Handler serves the interface as a single-page application: existing
// files are served as they are, every other path gets index.html so that
// client-side routes survive a reload.
//
// basePath is the URL prefix the application is published under by a
// reverse proxy that strips it ("/hls-monitor"), or "" for the root. The
// page learns it from a <base> element, which also makes its relative
// asset links resolve under the prefix.
func Handler(basePath string) http.Handler {
	root, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	index, err := fs.ReadFile(root, "index.html")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(notBuilt))
		})
	}
	index = bytes.Replace(index, []byte("<head>"), []byte(`<head><base href="`+html.EscapeString(basePath)+`/">`), 1)
	files := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p != "" && p != "index.html" {
			if st, err := fs.Stat(root, p); err == nil && !st.IsDir() {
				if strings.HasPrefix(p, "assets/") {
					// Build output under assets/ has content hashes in file names.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}

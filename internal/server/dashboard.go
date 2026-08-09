package server

import (
	"embed"
	"io/fs"
	"net/http"
)

// The dashboard is a React app in ui/, built by `task ui` into public/ and
// embedded here — so the binary stays the only artifact and needs no node at
// runtime. The build output is committed, so `go build` works on a clone
// without a JS toolchain.
//
//go:embed all:public
var dashboardFS embed.FS

// dashboardHandler serves the built app: "/" is index.html (the app is a
// single page and reads its token from the query string), anything else must
// be a real embedded asset — unknown paths stay 404s, as they were before the
// dashboard grew a build step.
func dashboardHandler() http.Handler {
	sub, err := fs.Sub(dashboardFS, "public")
	if err != nil {
		panic("dashboard: " + err.Error()) // build-time invariant, not a request path
	}
	files := http.FileServer(http.FS(sub))
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic("dashboard: " + err.Error())
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			if _, err := fs.Stat(sub, r.URL.Path[1:]); err != nil {
				http.NotFound(w, r)
				return
			}
			// Content-hashed filenames — safe to cache hard.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
}

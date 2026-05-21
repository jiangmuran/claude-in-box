package server

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// staticUI is the production Svelte build output, embedded at compile time.
// The build pipeline (docker/Dockerfile or scripts/build.sh) runs
// `npm --prefix web run build` first so this directory exists.
//
//go:embed all:webdist
var staticUI embed.FS

// uiHandler returns the static file server for the embedded UI when a real
// Vite build has been staged into webdist/. When only the placeholder
// .gitkeep exists (e.g. `go run` without first running `scripts/build-web.sh`
// or CI without the web job feeding dist), it returns nil so the caller
// falls back to the inline placeholder index.
//
// The presence check is on `index.html`, not just "any entry", because
// go:embed will happily slurp .gitkeep into the FS.
func uiHandler() http.Handler {
	sub, err := fs.Sub(staticUI, "webdist")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	fsh := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(sub, path); err != nil {
			// SPA fallback: any unknown path returns index.html so
			// client-side routing can take over.
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fsh.ServeHTTP(w, r2)
			return
		}
		fsh.ServeHTTP(w, r)
	})
}

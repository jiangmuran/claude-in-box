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

// uiHandler returns the static file server for the embedded UI. When the
// build artifacts are absent (e.g. `go run ./cmd/cib` without first running
// `npm run build`), it returns nil and the caller falls back to the
// placeholder index.
func uiHandler() http.Handler {
	sub, err := fs.Sub(staticUI, "webdist")
	if err != nil {
		return nil
	}
	entries, err := fs.ReadDir(sub, ".")
	if err != nil || len(entries) == 0 {
		return nil
	}
	// SPA-style: any unknown path returns index.html so client-side routing
	// can take over.
	fsh := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// API routes are matched first by the mux; if we are reached, this is
		// a UI request.
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(sub, path); err != nil {
			// Fall back to index.html for SPA paths.
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fsh.ServeHTTP(w, r2)
			return
		}
		fsh.ServeHTTP(w, r)
	})
}

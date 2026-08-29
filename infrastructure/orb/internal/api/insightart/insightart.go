// Package insightart holds the insight card banners and serves them.
//
// The app asks for three URLs per card (phone_1x/2x/3x) and fetches them
// directly, which used to mean Hello's `hello-data` S3 bucket. That bucket is
// private now and the artwork did not survive anywhere we hold; the search is
// written up in knowledgebase/CONSOLIDATION-PLAN.md under "The insight card art
// is gone". These are replacements drawn by scripts/insight-art/generate.py.
//
// Embedded rather than installed alongside the binary, because a deploy is one
// file today and adding a second thing to copy is how it silently stops being
// one file. It costs about 2 MB of binary.
package insightart

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed *.png
var files embed.FS

// Handler serves the embedded art.
//
// Long, immutable caching: the bytes for a given name never change, and a
// changed drawing gets a new binary anyway. Without this the app refetches
// every banner on every feed load over the LAN.
func Handler(prefix string) http.Handler {
	fileServer := http.FileServer(http.FS(files))
	return http.StripPrefix(prefix, http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			// Reject anything that is not one of ours before touching the FS,
			// so a probe cannot enumerate the package by timing or error text.
			name := strings.TrimPrefix(r.URL.Path, "/")
			if !Has(name) {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			fileServer.ServeHTTP(w, r)
		}))
}

// Has reports whether a file is one of the embedded banners.
func Has(name string) bool {
	if name == "" || strings.Contains(name, "/") {
		return false
	}
	f, err := files.Open(name)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// Names lists every embedded file, for tests and for the health check.
func Names() []string {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

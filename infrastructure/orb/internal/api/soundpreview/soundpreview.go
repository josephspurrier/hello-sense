// Package soundpreview holds the alarm ringtone and sleep sound previews and
// serves them.
//
// The originals lived in Hello's `hello-audio` S3 bucket, which is empty; the
// search that established that is written up in knowledgebase/GOING-PUBLIC.md.
// These were recovered from a Sense's own SD card in 2026: the twelve
// SLPTONES files verify byte-exact against the SHA1s the `file_info_one_five`
// table records, which also vouches for the fifteen RINGTONE files recovered
// alongside them.
//
// The files are named by their on-device stem (DIG002.mp3 for
// /RINGTONE/DIG002.raw, ST010.mp3 for /SLPTONES/ST010.RAW) rather than by
// display name, so the preview a phone fetches is tied to the file the Sense
// will actually play. The device format is headerless 16-bit little-endian
// PCM, mono, 32 kHz (kasetsu's ConvertToPcm.sh and AUDIO_SAMPLE_RATE in the
// firmware agree); ringtone previews are the full tone, sleep previews are 30
// seconds with a fade because the device loops them indefinitely, with the
// very short loops (White Noise is 1.3 s) repeated to fill.
//
// Embedded rather than installed alongside the binary, for the same reason as
// insightart: a deploy is one file, and a second thing to copy is how it
// silently stops being one file. It costs about 11 MB of binary, which is the
// price of the app's preview buttons doing anything at all.
package soundpreview

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed *.mp3
var files embed.FS

// Handler serves the embedded audio.
//
// Long, immutable caching, exactly like the card banners: the bytes for a
// given name never change, and a re-encoded preview arrives in a new binary
// anyway. The app fetches these with a plain player URL that carries no auth
// token, which is also how it fetched them from S3 originally.
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

// Has reports whether a file is one of the embedded previews.
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

// Names lists every embedded file, for tests.
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

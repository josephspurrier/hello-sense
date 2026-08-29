package edge

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// firmwareName is what a servable image may be called.
//
// Deliberately narrow. This path hands bytes to a device that will write them
// to its own flash and then boot them, so the filename comes from a fixed
// alphabet and nothing else: no separators, no dots-dots, no percent escapes
// that could decode into either.
var firmwareName = regexp.MustCompile(`^[A-Za-z0-9._-]+\.bin$`)

// firmware serves an OTA image.
//
// The client is the CC3200's hand-rolled downloader (kitsune/fatfs_cmd.c,
// GetData), which is stricter than it looks. It scans the first packet for the
// literal "200 OK", reads the size from a Content-Length header, and gives up
// on a chunked response or a "Connection: close". http.ServeContent satisfies
// all three for a seekable file, which is why this does not write the body
// itself.
//
// Every request is logged at WARN. An unexpected firmware download is the most
// consequential thing this server can be asked for, and it should be visible
// without turning on debug logging first.
func (h *Handler) firmware(w http.ResponseWriter, r *http.Request) {
	if h.FirmwareDir == "" {
		// Not configured, so the route does not exist rather than existing and
		// failing. A device that gets a 404 here logs HTTP_FILE_NOT_FOUND and
		// abandons the download without touching its boot record.
		http.NotFound(w, r)
		return
	}

	name := r.PathValue("name")
	if !firmwareName.MatchString(name) || strings.Contains(name, "..") {
		h.log.Warn("firmware request refused", "name", name, "remote", r.RemoteAddr)
		http.NotFound(w, r)
		return
	}

	full := filepath.Join(h.FirmwareDir, name)
	f, err := os.Open(full)
	if err != nil {
		h.log.Warn("firmware not found", "name", name, "err", err)
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		h.log.Warn("firmware not a file", "name", name)
		http.NotFound(w, r)
		return
	}

	h.log.Warn("SERVING FIRMWARE", "name", name, "bytes", fi.Size(),
		"remote", r.RemoteAddr, "device", r.Header.Get(senseIDHeader))

	// Explicit, rather than left to sniffing: the device does not care about
	// the type, but a stable Content-Type keeps the header block small and
	// predictable, and the whole block has to land in the device's first recv.
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, name, fi.ModTime().UTC().Truncate(time.Second), f)
}

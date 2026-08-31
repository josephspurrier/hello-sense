package edge

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// maxExport caps a single pushed file. Generous: the largest thing the device
// is likely to push is an SD-card sleep tone (raw 16 kHz audio, a few MB), and
// this path exists to recover exactly those. It still bounds the write so a
// runaway or hostile upload cannot fill the disk.
const maxExport = 64 << 20

// exportName is what a pushed file may be called. As narrow as firmwareName:
// this writes bytes from a device to a file on the server, so the name comes
// from a fixed alphabet and nothing else. No separators, so a name can never
// escape ExportDir.
var exportName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// export receives a file the device pushes off its own storage.
//
// The Sense's `x` console command can pipe a file, a serial-flash blob or an
// SD-card file such as a sleep tone, into an HTTP POST. This is the landing
// zone for that: it writes the request body to ExportDir/<name>. The body has
// already been de-chunked by the front proxy (the device sends it
// Transfer-Encoding: chunked), so it arrives here as the exact file bytes.
//
// Off by default, exactly like firmware serving: an empty ExportDir makes the
// route 404 rather than existing and writing files somewhere. A server that
// cannot accept a pushed file cannot be talked into writing one, which is the
// right default for a path that takes bytes from a device and commits them to
// disk. Turn it on (ORB_EXPORT_DIR) only while recovering files, then off.
//
// Logged at WARN. Accepting bytes from a device and writing them to disk is
// consequential enough to be visible without turning on debug logging first.
func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	if h.ExportDir == "" {
		http.NotFound(w, r)
		return
	}

	name := r.PathValue("name")
	if !exportName.MatchString(name) || strings.Contains(name, "..") {
		h.log.Warn("export refused", "name", name, "remote", r.RemoteAddr)
		http.NotFound(w, r)
		return
	}

	full := filepath.Join(h.ExportDir, name)
	f, err := os.Create(full)
	if err != nil {
		h.fail(w, "create export file", err, http.StatusInternalServerError)
		return
	}
	defer f.Close()

	// LimitReader, not the whole body: a device that never sends the chunked
	// terminator would otherwise stream forever, and the cap is also the only
	// bound on how much disk one push can take.
	n, err := io.Copy(f, io.LimitReader(r.Body, maxExport))
	if err != nil {
		// The partial file is left in place on purpose: a truncated export is
		// still worth inspecting, and this path is a manual recovery tool whose
		// operator can see the byte count against what they expected.
		h.log.Warn("export write failed", "name", name, "bytes", n, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}

	h.log.Warn("RECEIVED EXPORT", "name", name, "bytes", n,
		"remote", r.RemoteAddr, "device", r.Header.Get(senseIDHeader))
	w.WriteHeader(http.StatusOK)
}

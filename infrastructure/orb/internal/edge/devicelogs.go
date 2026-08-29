package edge

import (
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/josephspurrier/hello-orb/orb/internal/sense"
)

// minLogRun is the shortest printable run worth reporting. Anything shorter is
// mostly protobuf framing bytes that happen to fall in the ASCII range.
const minLogRun = 4

// deviceLogs accepts the device's own log upload, and under -debug prints it.
//
// The body is a `sense_log` protobuf, framed like every other device payload.
// Rather than carry a .proto for a message used nowhere else, this pulls the
// printable runs out of the payload: the log text is the only string field of
// any length, so what comes out is the device's own words with a little framing
// noise around them. Enough to answer "what did it do", and far better than
// discarding it.
//
// Written after an OTA was offered, downloaded and then silently not applied.
// The server saw a successful GET and nothing else; the device had posted 2117
// bytes explaining itself, straight into io.Discard.
func (h *Handler) deviceLogs(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))

	// Answer first, and identically either way. This endpoint must never fail a
	// device because a diagnostic aid had a problem.
	w.WriteHeader(http.StatusNoContent)

	if err != nil || !h.log.Enabled(r.Context(), slog.LevelDebug) {
		return
	}

	payload := body
	if p, _, _, perr := sense.ParseSigned(body); perr == nil {
		payload = p
	}

	for _, run := range printableRuns(payload) {
		h.log.Debug("device log", "device", r.Header.Get(senseIDHeader), "text", run)
	}
}

// printableRuns returns the runs of printable ASCII at least minLogRun long.
func printableRuns(b []byte) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() >= minLogRun {
			if s := strings.TrimSpace(cur.String()); s != "" {
				out = append(out, s)
			}
		}
		cur.Reset()
	}
	for _, c := range b {
		if c == '\n' || c == '\r' {
			flush()
			continue
		}
		if c >= 0x20 && c < 0x7f {
			cur.WriteByte(c)
			continue
		}
		flush()
	}
	flush()
	return out
}

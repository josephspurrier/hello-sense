package edge

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func firmwareHandler(t *testing.T, dir string) *Handler {
	t.Helper()
	return &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil)), FirmwareDir: dir}
}

// The device abandons a download on 404 without touching its boot record, so
// refusing is always safe. Serving the wrong bytes is not.
func TestFirmwareRefusesEverythingButAPlainName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kitsune.bin"), []byte("IMAGE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.key"), []byte("MUST NOT SERVE"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := firmwareHandler(t, dir)

	for _, c := range []struct {
		name string
		want int
	}{
		{"kitsune.bin", 200},
		{"secret.key", 404}, // not a .bin
		{"..%2Fsecret.key", 404},
		{"nope.bin", 404}, // does not exist
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/firmware/"+c.name, nil)
			req.SetPathValue("name", c.name)
			h.firmware(rec, req)
			if rec.Code != c.want {
				t.Fatalf("got %d, want %d", rec.Code, c.want)
			}
			if strings.Contains(rec.Body.String(), "MUST NOT SERVE") {
				t.Fatal("served a file outside the allowed shape")
			}
		})
	}
}

// Unconfigured must not serve, whatever is on disk.
func TestFirmwareOffByDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kitsune.bin"), []byte("IMAGE"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := firmwareHandler(t, "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/firmware/kitsune.bin", nil)
	req.SetPathValue("name", "kitsune.bin")
	h.firmware(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("served firmware with no FirmwareDir configured: %d", rec.Code)
	}
}

// The CC3200's downloader scans the first packet for "200 OK" and reads the
// size from Content-Length. It gives up on chunked and on Connection: close.
// This pins the response shape those constraints require.
func TestFirmwareResponseShapeSuitsTheDevice(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("A", 146928)
	if err := os.WriteFile(filepath.Join(dir, "kitsune.bin"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	h := firmwareHandler(t, dir)

	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/firmware/kitsune.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if resp.ContentLength != int64(len(body)) {
		t.Fatalf("Content-Length %d, want %d", resp.ContentLength, len(body))
	}
	if len(resp.TransferEncoding) != 0 {
		t.Fatalf("chunked response: %v; the device gives up on chunked", resp.TransferEncoding)
	}
	if resp.Close {
		t.Fatal("Connection: close; the device explicitly does not support it")
	}
	got, _ := io.ReadAll(resp.Body)
	if len(got) != len(body) {
		t.Fatalf("body %d bytes, want %d", len(got), len(body))
	}
}

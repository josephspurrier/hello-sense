package edge_test

// End-to-end test of the ingest path against the real server, using a real
// captured device payload re-signed with a known key.
//
// This drives an actual http.Server rather than calling the handler directly,
// so it covers routing, the signature framing, protobuf decode, the clock
// guard, and the shape of the reply the device has to accept.
//
// Requires the orb database. Skips without it, so `go test ./...` still passes
// on a machine with no stack running.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/josephspurrier/hello-orb/orb/internal/edge"
	pbdev "github.com/josephspurrier/hello-orb/orb/internal/pb/device"
	pbntp "github.com/josephspurrier/hello-orb/orb/internal/pb/ntp"
	"github.com/josephspurrier/hello-orb/orb/internal/pb/worker"
	"github.com/josephspurrier/hello-orb/orb/internal/sense"
	"github.com/josephspurrier/hello-orb/orb/internal/store"
)

const testDSN = "postgres://hello:hello@localhost:5432/orb"

func newServer(t *testing.T, shadow bool) (*httptest.Server, *store.Store) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, testDSN)
	if err != nil {
		t.Skipf("no orb database available: %v", err)
	}
	h := edge.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.ReadOnly = shadow
	srv := httptest.NewServer(h.Routes())
	t.Cleanup(func() { srv.Close(); st.Close() })
	return srv, st
}

// capturedBatch pulls a real batched_periodic_data out of the Kinesis capture.
func capturedBatch(t *testing.T) *pbdev.BatchedPeriodicData {
	t.Helper()
	f, err := os.Open("../sense/testdata/kinesis-sense-records.json")
	if err != nil {
		t.Skip("no capture available")
	}
	defer f.Close()
	var c struct {
		Records []struct{ Data string } `json:"Records"`
	}
	if err := json.NewDecoder(f).Decode(&c); err != nil {
		t.Fatal(err)
	}
	for _, rec := range c.Records {
		raw, err := base64.StdEncoding.DecodeString(rec.Data)
		if err != nil {
			continue
		}
		var w worker.BatchPeriodicDataWorker
		if proto.Unmarshal(raw, &w) != nil || w.GetData() == nil {
			continue
		}
		// The worker and device packages declare the same message in different
		// proto packages, so they are distinct Go types. Round-tripping through
		// the wire converts between them, and incidentally proves the two
		// definitions really are byte compatible.
		b := w.GetData()
		if len(b.GetData()) == 0 {
			continue
		}
		wire, err := proto.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		var out pbdev.BatchedPeriodicData
		if err := proto.Unmarshal(wire, &out); err != nil {
			t.Fatalf("worker and device definitions are not wire compatible: %v", err)
		}
		return &out
	}
	t.Skip("capture contained no usable batch")
	return nil
}

// deviceKey reads the AES key for the captured device, so the request is signed
// exactly as the real device would sign it.
func deviceKey(t *testing.T, st *store.Store, deviceID string) []byte {
	t.Helper()
	dev, err := st.SenseByID(context.Background(), deviceID)
	if err != nil {
		t.Skipf("device %s not in orb db: %v", deviceID, err)
	}
	return dev.AESKey
}

// signAsDevice builds a body in the device's layout: payload then IV then sig.
func signAsDevice(t *testing.T, key, payload []byte) []byte {
	t.Helper()
	signed, err := sense.Sign(key, payload) // returns IV|sig|payload
	if err != nil {
		t.Fatal(err)
	}
	trailer := signed[:sense.TrailerLength]
	return append(append([]byte{}, payload...), trailer...)
}

func TestSenseBatchAcceptsRealPayload(t *testing.T) {
	srv, st := newServer(t, true) // shadow: verify the path without writing
	batch := capturedBatch(t)
	key := deviceKey(t, st, batch.GetDeviceId())

	// Restamp the samples to now. The capture is hours old and the handler
	// deliberately drops anything more than two hours from the server clock.
	now := time.Now().UTC()
	for i, d := range batch.GetData() {
		d.UnixTime = proto.Int32(int32(now.Add(-time.Duration(i) * time.Minute).Unix()))
	}

	payload, err := proto.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	body := signAsDevice(t, key, payload)

	req, _ := http.NewRequest("POST", srv.URL+"/in/sense/batch", bytes.NewReader(body))
	req.Host = "sense-in.hello.is"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, b)
	}

	// The reply must carry a valid signature in the server layout, or the
	// device rejects it with "signature validation fail".
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < sense.TrailerLength {
		t.Fatalf("reply too short to be signed: %d bytes", len(out))
	}
	iv, sig, respPayload := out[:sense.IVLength], out[sense.IVLength:sense.TrailerLength], out[sense.TrailerLength:]
	if err := sense.Verify(key, respPayload, iv, sig); err != nil {
		t.Fatalf("device would reject our reply: %v", err)
	}
}

func TestSenseBatchRejectsBadSignature(t *testing.T) {
	srv, st := newServer(t, true)
	batch := capturedBatch(t)
	_ = deviceKey(t, st, batch.GetDeviceId())

	payload, _ := proto.Marshal(batch)
	body := signAsDevice(t, []byte("wrongkeywrongkey"), payload)

	req, _ := http.NewRequest("POST", srv.URL+"/in/sense/batch", bytes.NewReader(body))
	req.Host = "sense-in.hello.is"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a body signed with the wrong key", resp.StatusCode)
	}
}

func TestSenseBatchRejectsUnknownDevice(t *testing.T) {
	srv, _ := newServer(t, true)

	batch := &pbdev.BatchedPeriodicData{
		DeviceId:        proto.String("DEADBEEFDEADBEEF"),
		FirmwareVersion: proto.Int32(4513),
	}
	payload, _ := proto.Marshal(batch)
	body := signAsDevice(t, []byte("1234567891234567"), payload)

	req, _ := http.NewRequest("POST", srv.URL+"/in/sense/batch", bytes.NewReader(body))
	req.Host = "sense-in.hello.is"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// 401 not 404: telling an unauthenticated caller which device ids exist is
	// an enumeration oracle.
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for an unknown device", resp.StatusCode)
	}
}

// TestTimeSyncSurvivesAProxyRewritingHost is the test that was missing when a
// clock outage went unnoticed for two hours.
//
// sense_server.py sets Host to the upstream's own address before forwarding, so
// orb saw "127.0.0.1:8081" rather than "time.hello.is" and 404'd every clock
// request the device made. hello-time had never inspected Host (its resource
// was @Path("/")), so nothing broke until orb took the hostname over.
//
// It was missed because cmd/timecheck sets Host explicitly, which tests orb in
// isolation and never exercises the path the device actually takes. A request
// carrying a device id is routed on that id now, so the assertion here is that
// a mangled Host cannot take the clock down again.
func TestTimeSyncSurvivesAProxyRewritingHost(t *testing.T) {
	srv, st := newServer(t, false)
	const deviceID = "49F277D951568DF3"
	key := deviceKey(t, st, deviceID)

	reqPayload, _ := proto.Marshal(&pbntp.NTPDataPacket{OriginTs: proto.Int64(1)})
	body := signAsDevice(t, key, reqPayload)

	req, _ := http.NewRequest("POST", srv.URL+"/", bytes.NewReader(body))
	// Exactly what the proxy sends: the upstream's address, not the device's.
	req.Host = "127.0.0.1:8081"
	req.Header.Set("X-Hello-Sense-Id", deviceID)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d (want 200), body = %s\n"+
			"a proxy that rewrites Host must not be able to break the clock", resp.StatusCode, b)
	}

	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) <= sense.TrailerLength {
		t.Fatalf("reply too short to carry a signed timestamp: %d bytes", len(out))
	}
	iv, sig, payload := out[:sense.IVLength], out[sense.IVLength:sense.TrailerLength], out[sense.TrailerLength:]
	if err := sense.Verify(key, payload, iv, sig); err != nil {
		t.Fatalf("device would reject the reply: %v", err)
	}
}

// A POST to "/" with no device id is still not a clock request, and must not be
// answered as one. Without this, the rule above would turn the catch-all into
// an unauthenticated endpoint that leaks whether a device id exists.
func TestBareSlashWithoutADeviceIDIsNotTimeSync(t *testing.T) {
	srv, _ := newServer(t, false)

	req, _ := http.NewRequest("POST", srv.URL+"/", bytes.NewReader([]byte("x")))
	req.Host = "127.0.0.1:8081"

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a bare POST / with no device id", resp.StatusCode)
	}
}

func TestTimeResponseIsSignedAndSane(t *testing.T) {
	key := []byte("1234567891234567")

	reqPayload, _ := proto.Marshal(&pbdev.BatchedPeriodicData{}) // any bytes; only framing matters
	reqBody := signAsDevice(t, key, reqPayload)

	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	out, err := edge.TimeResponse(key, reqBody, now)
	if err != nil {
		t.Fatal(err)
	}
	iv, sig, payload := out[:sense.IVLength], out[sense.IVLength:sense.TrailerLength], out[sense.TrailerLength:]
	if err := sense.Verify(key, payload, iv, sig); err != nil {
		t.Fatalf("device would reject the time reply: %v", err)
	}

	if len(payload) == 0 {
		t.Fatal("empty time payload")
	}
}

// TestTimeResponseDecodesToNow decodes the reply the way the firmware does.
//
// This is the assertion that matters, and the one that was missing. The old
// test stopped at "the reply is signed", which a reply carrying any timestamp
// at all will pass. Two separate bugs live in this handler's arithmetic:
//
//   - Unix epoch where the device expects NTP, which put it in 1956.
//   - A sign conversion that cleared the top bit instead of reinterpreting it,
//     which put it in 1958. Caught only by diffing against the live Java
//     service, because nothing here decoded the number.
//
// A wrong clock here is not a cosmetic defect. The device stamps every sample
// it uploads against the clock it was handed, so one bad reply silently
// corrupts a whole night.
func TestTimeResponseDecodesToNow(t *testing.T) {
	key := []byte("1234567891234567")

	reqPayload, _ := proto.Marshal(&pbntp.NTPDataPacket{OriginTs: proto.Int64(42)})
	reqBody := signAsDevice(t, key, reqPayload)

	now := time.Date(2026, 8, 28, 23, 31, 58, 0, time.UTC)
	out, err := edge.TimeResponse(key, reqBody, now)
	if err != nil {
		t.Fatal(err)
	}

	var pkt pbntp.NTPDataPacket
	if err := proto.Unmarshal(out[sense.TrailerLength:], &pkt); err != nil {
		t.Fatal(err)
	}

	if pkt.GetOriginTs() != 42 {
		t.Errorf("origin_ts = %d, want the request's 42 echoed back", pkt.GetOriginTs())
	}

	// Present-day NTP timestamps have the top bit set, so the signed field is
	// negative, exactly as Java's TimeStamp.ntpValue() long is. A positive
	// value here means the sign bit was cleared rather than reinterpreted.
	for _, f := range []struct {
		name string
		got  int64
	}{
		{"reference_ts", pkt.GetReferenceTs()},
		{"receive_ts", pkt.GetReceiveTs()},
		{"transmit_ts", pkt.GetTransmitTs()},
	} {
		if f.got >= 0 {
			t.Errorf("%s = %d, want negative; the sign bit was cleared, not reinterpreted", f.name, f.got)
		}
		// Decode the fixed-point value back the way the firmware does.
		secs := int64(uint64(f.got)>>32) - 2208988800
		if got := time.Unix(secs, 0).UTC(); !got.Equal(now) {
			t.Errorf("%s decodes to %s, want %s", f.name, got, now)
		}
	}
}

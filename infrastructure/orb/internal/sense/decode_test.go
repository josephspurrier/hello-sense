package sense_test

// Validates the Go wire understanding against production output.
//
// The Kinesis state file captured on 2026-08-13 holds real records that
// suripu-service wrote after parsing genuine device uploads. Each is a
// BatchPeriodicDataWorker wrapping the exact batched_periodic_data the Sense
// sent. Decoding those in Go and comparing field by field against the values
// Java stored in DynamoDB (now migrated into orb.sensor_samples) is the closest
// thing available to a conformance test, and it needs no device.
//
// Skips if the capture is absent, so the suite still runs on a clean checkout.

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/josephspurrier/hello-orb/orb/internal/pb/worker"
	"github.com/josephspurrier/hello-orb/orb/internal/sense"
)

const capturePath = "testdata/kinesis-sense-records.json"

type capture struct {
	Records []struct {
		Data string `json:"Data"`
	} `json:"Records"`
}

func loadCapture(t *testing.T) capture {
	t.Helper()
	f, err := os.Open(filepath.FromSlash(capturePath))
	if os.IsNotExist(err) {
		t.Skipf("no capture at %s; run scripts/extract-kinesis-records.sh", capturePath)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var c capture
	if err := json.NewDecoder(f).Decode(&c); err != nil {
		t.Fatal(err)
	}
	if len(c.Records) == 0 {
		t.Skip("capture is empty")
	}
	return c
}

// TestDecodeRealUploads decodes every captured record and asserts the values
// are self-consistent and in plausible ranges. It is deliberately strict about
// the things that would silently corrupt a migration: the device id, the
// timestamp, and the sensor scaling.
func TestDecodeRealUploads(t *testing.T) {
	c := loadCapture(t)

	var samples int
	for i, rec := range c.Records {
		raw, err := base64.StdEncoding.DecodeString(rec.Data)
		if err != nil {
			t.Fatalf("record %d: base64: %v", i, err)
		}

		var w worker.BatchPeriodicDataWorker
		if err := proto.Unmarshal(raw, &w); err != nil {
			t.Fatalf("record %d: unmarshal worker: %v", i, err)
		}

		batch := w.GetData()
		if batch == nil {
			t.Fatalf("record %d: no batched_periodic_data", i)
		}
		if got := batch.GetDeviceId(); got == "" {
			t.Fatalf("record %d: empty device_id", i)
		}
		if fw := batch.GetFirmwareVersion(); fw <= 0 {
			t.Errorf("record %d: implausible firmware_version %d", i, fw)
		}

		for j, d := range batch.GetData() {
			samples++

			// unix_time is an int32 of seconds. The Orb's clock starts about 70
			// years in the past after a reboot, which is exactly the failure
			// this check would catch on real data.
			ts := time.Unix(int64(d.GetUnixTime()), 0).UTC()
			if ts.Year() < 2020 || ts.Year() > 2100 {
				t.Errorf("record %d sample %d: unix_time %d is %s, device clock unsynced?",
					i, j, d.GetUnixTime(), ts)
			}

			// Temperature and humidity are centi-units on the wire. A plain
			// degrees value here would mean the scaling assumption in the orb
			// schema is wrong.
			if tmp := d.GetTemperature(); tmp < -5000 || tmp > 8000 {
				t.Errorf("record %d sample %d: temperature %d outside centi-degree range", i, j, tmp)
			}
			if hum := d.GetHumidity(); hum < 0 || hum > 10000 {
				t.Errorf("record %d sample %d: humidity %d outside centi-percent range", i, j, hum)
			}
			if l := d.GetLight(); l < 0 {
				t.Errorf("record %d sample %d: negative light %d", i, j, l)
			}
		}
	}

	if samples == 0 {
		t.Fatal("decoded no samples from the capture")
	}
	t.Logf("decoded %d records, %d samples", len(c.Records), samples)
}

// TestSignRoundTrip covers the framing in both directions. The asymmetry
// (device appends IV+sig, server prepends them) is the part most likely to be
// got backwards, so it is asserted explicitly rather than only round-tripped.
func TestSignRoundTrip(t *testing.T) {
	key := []byte("1234567891234567") // firmware default, 16 bytes
	payload := []byte("some protobuf bytes")

	signed, err := sense.Sign(key, payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(signed) != sense.TrailerLength+len(payload) {
		t.Fatalf("signed length = %d, want %d", len(signed), sense.TrailerLength+len(payload))
	}
	// Server to device: IV and signature FIRST.
	if got := signed[sense.TrailerLength:]; string(got) != string(payload) {
		t.Fatalf("payload should follow the trailer, got %q", got)
	}

	// Device to server: payload FIRST, trailer appended. Build one and verify.
	iv := signed[:sense.IVLength]
	sig := signed[sense.IVLength:sense.TrailerLength]
	deviceStyle := append(append([]byte{}, payload...), append(iv, sig...)...)

	gotPayload, gotIV, gotSig, err := sense.ParseSigned(deviceStyle)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotPayload) != string(payload) {
		t.Fatalf("ParseSigned payload = %q, want %q", gotPayload, payload)
	}
	if err := sense.Verify(key, gotPayload, gotIV, gotSig); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	// A single flipped byte must fail.
	tampered := append([]byte{}, gotPayload...)
	tampered[0] ^= 0x01
	if err := sense.Verify(key, tampered, gotIV, gotSig); err == nil {
		t.Fatal("Verify accepted a tampered payload")
	}
}

func TestParseSignedRejectsShortBody(t *testing.T) {
	if _, _, _, err := sense.ParseSigned(make([]byte, sense.TrailerLength-1)); err == nil {
		t.Fatal("expected error for body shorter than the trailer")
	}
}

// TestVerifyIgnoresPaddingBytes is a regression test for the bug that live
// traffic exposed. The device leaves the 12 bytes after the SHA1 as whatever
// was in its buffer, and suripu only ever compares the first 20. An
// implementation that re-encrypts a zero-padded hash and compares ciphertext
// rejects every genuine upload while passing its own self-signed test data,
// which is exactly how this went unnoticed until it saw a real Orb.
func TestVerifyIgnoresPaddingBytes(t *testing.T) {
	key := []byte("1234567891234567")
	payload := []byte("a device payload")

	iv := bytes.Repeat([]byte{0x42}, sense.IVLength)

	// Build a signature the way the firmware does: SHA1 then 12 bytes of
	// whatever, encrypted. Non-zero padding is the whole point.
	sum := sha1.Sum(payload)
	plain := make([]byte, sense.SigLength)
	copy(plain, sum[:])
	copy(plain[len(sum):], []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08})

	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, sense.SigLength)
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(sig, plain)

	if err := sense.Verify(key, payload, iv, sig); err != nil {
		t.Fatalf("Verify rejected a signature with non-zero padding: %v", err)
	}

	// The 20 bytes that matter must still be enforced.
	if err := sense.Verify(key, []byte("different payload"), iv, sig); err == nil {
		t.Fatal("Verify accepted a signature for the wrong payload")
	}
}

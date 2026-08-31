package pill_test

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/josephspurrier/hello-orb/orb/internal/pill"
)

var testKey = []byte("677089B5E45550EC") // 16 bytes; shape matches a real pill key

// encryptLikePill builds a payload the way the pill does: an 8-byte nonce
// followed by AES-CTR ciphertext, with the nonce zero-padded to a counter block.
func encryptLikePill(t *testing.T, key, nonce, plain []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	iv := make([]byte, aes.BlockSize)
	copy(iv, nonce)
	out := make([]byte, len(plain))
	cipher.NewCTR(block, iv).XORKeyStream(out, plain)
	return append(append([]byte{}, nonce...), out...)
}

func TestDecryptDecodeV2(t *testing.T) {
	// A plausible v2 body: amplitude, range, kickoffs, duration, magic bytes.
	body := make([]byte, 10)
	binary.LittleEndian.PutUint32(body[0:4], 12_000_000)
	binary.LittleEndian.PutUint16(body[4:6], 4096)
	body[6] = 3
	body[7] = 42
	body[8], body[9] = 0x5A, 0x5A

	payload := encryptLikePill(t, testKey, []byte("12345678"), body)

	decrypted, err := pill.Decrypt(testKey, payload)
	if err != nil {
		t.Fatal(err)
	}
	m, err := pill.Decode(2, decrypted)
	if err != nil {
		t.Fatal(err)
	}

	if m.MotionRange != 4096 {
		t.Errorf("MotionRange = %d, want 4096", m.MotionRange)
	}
	if m.KickoffCounts != 3 {
		t.Errorf("KickoffCounts = %d, want 3", m.KickoffCounts)
	}
	if m.OnDurationSecs != 42 {
		t.Errorf("OnDurationSecs = %d, want 42", m.OnDurationSecs)
	}

	// sqrt(12e6) * (4*9.81/65536) - 9.81, in milli-units.
	// = 3464.1016 * 0.000598754... - 9.81 = 2.0739... - 9.81 = -7.736...
	if m.SVMNoGravity > -7000 || m.SVMNoGravity < -8500 {
		t.Errorf("SVMNoGravity = %d, outside the expected range for this amplitude", m.SVMNoGravity)
	}
}

// TestRawToMilliMS2Scaling pins the conversion constants. If someone "tidies"
// accRangeG or accResolution, every historical sample silently changes meaning,
// so this asserts a known point rather than trusting the formula to stay put.
func TestRawToMilliMS2Scaling(t *testing.T) {
	cases := []struct {
		raw      uint32
		wantLow  int64
		wantHigh int64
		desc     string
	}{
		{0, -9811, -9809, "no motion is negative gravity"},
		{265_000_000, -100, 100, "around 1g reads near zero"},
	}
	for _, c := range cases {
		body := make([]byte, 6)
		binary.LittleEndian.PutUint32(body[0:4], c.raw)
		body[4], body[5] = 0x5A, 0x5A

		m, err := pill.Decode(1, body)
		if err != nil {
			t.Fatalf("%s: %v", c.desc, err)
		}
		if m.SVMNoGravity < c.wantLow || m.SVMNoGravity > c.wantHigh {
			t.Errorf("%s: raw %d -> %d, want between %d and %d",
				c.desc, c.raw, m.SVMNoGravity, c.wantLow, c.wantHigh)
		}
	}
}

func TestDecryptRejectsShortPayload(t *testing.T) {
	if _, err := pill.Decrypt(testKey, []byte("short")); !errors.Is(err, pill.ErrShortPayload) {
		t.Fatalf("err = %v, want ErrShortPayload", err)
	}
}

func TestDecodeRejectsUnknownVersion(t *testing.T) {
	body := make([]byte, 10)
	body[8], body[9] = 0x5A, 0x5A
	// Version 5 does not exist, so it must fail loudly rather than mis-decode.
	if _, err := pill.Decode(5, body); !errors.Is(err, pill.ErrVersion) {
		t.Fatalf("err = %v, want ErrVersion", err)
	}
}

// TestDecryptDecodeV4 covers the 1.5 pill (pillx_DVT1) condensed payload:
// max(u8), cos_theta(u8), motion_mask(u64 LE), and no 0x5A magic trailer.
func TestDecryptDecodeV4(t *testing.T) {
	body := make([]byte, 10)
	body[0] = 100                                              // max
	body[1] = 200                                              // cos_theta
	binary.LittleEndian.PutUint64(body[2:10], 0x00F0F0F0F0F0F0) // motion mask, high bits clear

	payload := encryptLikePill(t, testKey, []byte("nonce123"), body)

	decrypted, err := pill.Decrypt(testKey, payload)
	if err != nil {
		t.Fatal(err)
	}
	m, err := pill.Decode(4, decrypted)
	if err != nil {
		t.Fatal(err)
	}

	// (100<<7)*countsInG*1000 = 7664.07; (7664 - 383) * 2 = 14562.
	if m.SVMNoGravity < 14500 || m.SVMNoGravity > 14620 {
		t.Errorf("SVMNoGravity = %d, want ~14562", m.SVMNoGravity)
	}
	if m.CosTheta != 200 {
		t.Errorf("CosTheta = %d, want 200", m.CosTheta)
	}
	if uint64(m.MotionMask) != 0x00F0F0F0F0F0F0 {
		t.Errorf("MotionMask = %016x, want 00f0f0f0f0f0f0", uint64(m.MotionMask))
	}
}

// TestDecodeV4SkipsMagic proves v4 does not apply the 0x5A magic check: a
// payload whose trailing bytes are not 0x5A (as a real motion mask usually is)
// must still decode, unlike v0..v3.
func TestDecodeV4SkipsMagic(t *testing.T) {
	body := make([]byte, 10)
	body[0] = 50
	binary.LittleEndian.PutUint64(body[2:10], 0x0102030405060708) // no 0x5A anywhere
	if _, err := pill.Decode(4, body); err != nil {
		t.Fatalf("v4 must not magic-check, got %v", err)
	}
}

// TestMagicByteQuirk documents suripu's actual behaviour, which this code
// matches on purpose. The check rejects only when BOTH trailing bytes differ
// from 0x5A. Tightening it would reject payloads the old system stored.
func TestMagicByteQuirk(t *testing.T) {
	half := make([]byte, 10)
	binary.LittleEndian.PutUint32(half[0:4], 1000)
	half[8], half[9] = 0x00, 0x5A // only one byte matches
	if _, err := pill.Decode(2, half); err != nil {
		t.Fatalf("one matching magic byte should pass, matching suripu: %v", err)
	}

	neither := make([]byte, 10)
	binary.LittleEndian.PutUint32(neither[0:4], 1000)
	neither[8], neither[9] = 0x00, 0x00
	if _, err := pill.Decode(2, neither); !errors.Is(err, pill.ErrMagicBytes) {
		t.Fatalf("no matching magic bytes should fail, got %v", err)
	}
}

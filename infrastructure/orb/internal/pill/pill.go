// Package pill decrypts and decodes Sleep Pill motion payloads.
//
// The pill's motion data is encrypted with the PILL's own key, not the Sense's.
// The Sense only relays it: it cannot read the contents. So ingesting pill data
// needs two keys, and a pill whose key is unknown produces samples that can be
// stored but never interpreted.
//
// Ported from TrackerMotion.Utils in suripu-core and checked against it field
// by field. The layouts are little endian and version dependent, and the
// version is carried in a field that suripu-api calls firmware_version and the
// newer proto calls protocol_version. It is neither: it is the payload format
// version. The pill's actual firmware build is a different field.
package pill

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

const (
	// nonceLen is how many leading bytes of the payload are the CTR nonce.
	nonceLen = 8

	// accRangeG, gravityMS2 and accResolution convert the pill's raw 32-bit
	// accelerometer counts to milli-m/s^2. These constants are part of the
	// stored value's meaning: change one and every historical sample silently
	// means something different.
	accRangeG     = 4.0
	gravityMS2    = 9.81
	accResolution = 65536.0

	// pill1p5MotionOffset and pill1p5MotionMultiplier offset and scale the v4
	// (1.5 pill) motion value, matching suripu's PILL_1P5_MOTION_OFFSET and
	// PILL_1P5_MOTION_MULTIPLIER. They were derived from comparing 1.0 and 1.5
	// pills side by side; changing them silently reinterprets every v4 sample.
	pill1p5MotionOffset     = 383
	pill1p5MotionMultiplier = 2
)

// countsInG is the scale factor from raw counts to g.
var countsInG = (accRangeG * gravityMS2) / accResolution

var (
	ErrShortPayload = errors.New("pill: payload shorter than the nonce")
	ErrMagicBytes   = errors.New("pill: trailing magic bytes do not match")
	ErrVersion      = errors.New("pill: unsupported payload version")
)

// Motion is one minute of pill movement.
//
// SVMNoGravity is "signal vector magnitude, gravity removed", the primary input
// to every sleep algorithm. The others are only populated by later payload
// versions and are zero otherwise.
type Motion struct {
	SVMNoGravity   int64
	MotionRange    int64
	KickoffCounts  int64
	OnDurationSecs int64

	// MotionMask and CosTheta are only present in v4 (1.5 pill) payloads and are
	// zero otherwise. MotionMask is a 60-bit-per-minute bitmask of which seconds
	// saw motion; because valid masks only ever set bits 0..59, it doubles as a
	// sanity check that the decryption key is correct (a wrong key yields random
	// high bits). CosTheta is the pill's stored orientation-change measure.
	MotionMask int64
	CosTheta   int64
}

// Decrypt reverses the pill's AES-CTR encryption.
//
// Layout is [nonce(8)][ciphertext...]. The nonce is zero-padded to a full
// 16-byte counter block; it is not a 16-byte IV truncated. A trailing CRC is
// present in some versions and deliberately ignored here, matching suripu,
// which never validated it either.
func Decrypt(key, payload []byte) ([]byte, error) {
	if len(payload) <= nonceLen {
		return nil, fmt.Errorf("%w: %d bytes", ErrShortPayload, len(payload))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("pill: cipher: %w", err)
	}
	iv := make([]byte, aes.BlockSize)
	copy(iv, payload[:nonceLen])

	ciphertext := payload[nonceLen:]
	out := make([]byte, len(ciphertext))
	cipher.NewCTR(block, iv).XORKeyStream(out, ciphertext)
	return out, nil
}

// Decode reads a decrypted payload according to its version.
//
// Versions 0 and 1 carry only an amplitude. Versions 2 and 3 add range, kickoff
// count and duration. Version 4 is the 1.5 pill (pillx_DVT1) condensed payload:
// max, cos(theta) and a per-second motion mask, with its own offset/scale. It is
// the only version without the 0x5A magic trailer.
func Decode(version int32, decrypted []byte) (Motion, error) {
	// Only v0..v3 append the 0x5A 0x5A magic trailer. The v4 (1.5 pill) payload
	// ends in the motion mask, so checking it for magic would reject every valid
	// sample; suripu likewise only magic-checks the pre-v4 decoders.
	if version < 4 {
		if err := checkMagic(decrypted); err != nil {
			return Motion{}, err
		}
	}

	switch version {
	case 0, 1:
		if len(decrypted) < 4 {
			return Motion{}, fmt.Errorf("pill: v%d needs 4 bytes, got %d", version, len(decrypted))
		}
		raw := uint64(binary.LittleEndian.Uint32(decrypted[0:4]))
		return Motion{SVMNoGravity: rawToMilliMS2(raw)}, nil

	case 2, 3:
		if len(decrypted) < 8 {
			return Motion{}, fmt.Errorf("pill: v%d needs 8 bytes, got %d", version, len(decrypted))
		}
		raw := uint64(binary.LittleEndian.Uint32(decrypted[0:4]))
		return Motion{
			SVMNoGravity:   rawToMilliMS2(raw),
			MotionRange:    int64(binary.LittleEndian.Uint16(decrypted[4:6])),
			KickoffCounts:  int64(decrypted[6]),
			OnDurationSecs: int64(decrypted[7]),
		}, nil

	case 4:
		// 1.5 pill condensed payload: max(u8), cos_theta(u8), motion_mask(u64 LE).
		// Mirrors suripu's decryptedToPillPayloadVersion3. The stored motion value
		// is max reconstructed to its ~16-bit range (<<7), converted to milli-m/s^2,
		// then offset and doubled to line the 1.5 pill up with the 1.0 pill.
		if len(decrypted) < 10 {
			return Motion{}, fmt.Errorf("pill: v4 needs 10 bytes, got %d", len(decrypted))
		}
		maxAccelMS2 := float64(int(decrypted[0])<<7) * countsInG
		svm := (int64(1000*maxAccelMS2) - pill1p5MotionOffset) * pill1p5MotionMultiplier
		return Motion{
			SVMNoGravity: svm,
			CosTheta:     int64(decrypted[1]),
			MotionMask:   int64(binary.LittleEndian.Uint64(decrypted[2:10])),
		}, nil

	default:
		return Motion{}, fmt.Errorf("%w: version %d", ErrVersion, version)
	}
}

// checkMagic mirrors suripu's checkForMagicBytes, including its oddity.
//
// The pill DVT appends 0x5A 0x5A. suripu rejects only when BOTH of the last two
// bytes differ from 0x5A, so a payload with one matching byte passes. That is
// almost certainly a bug (it reads like it meant ||), but matching it exactly
// matters: tightening the check here would reject payloads the old system
// accepted and stored, and this code has to agree with 20,000 existing rows.
func checkMagic(b []byte) error {
	if len(b) > 4 && b[len(b)-1] != 0x5A && b[len(b)-2] != 0x5A {
		return ErrMagicBytes
	}
	return nil
}

// rawToMilliMS2 converts raw accelerometer counts to milli-m/s^2 with gravity
// subtracted. Integer truncation, not rounding, to match Java's (long) cast.
func rawToMilliMS2(raw uint64) int64 {
	ms2 := math.Sqrt(float64(raw))*countsInG - gravityMS2
	return int64(ms2 * 1000)
}

package speech

import (
	"encoding/binary"
	"math"
	"testing"
)

// encodeSpeechRequest builds a SpeechRequest protobuf by hand (all varint
// fields), the mirror of parseSpeechRequest, so the tests craft what the device
// sends.
func encodeSpeechRequest(word Keyword, samplingRate int32) []byte {
	var b []byte
	put := func(field, v uint64) {
		b = appendUvarint(b, field<<3) // wire type 0
		b = appendUvarint(b, v)
	}
	put(1, uint64(word))
	put(6, uint64(samplingRate))
	return b
}

func appendUvarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func TestParseRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef") // 16 bytes
	pb := encodeSpeechRequest(KeywordOKSense, 16000)
	audio := []byte{0x12, 0x34, 0x56, 0x78}

	body := Sign(key, pb, audio)
	req, err := Parse(key, body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if req.Word != KeywordOKSense {
		t.Errorf("word = %d, want OK_SENSE", req.Word)
	}
	if req.SamplingRate != 16000 {
		t.Errorf("sampling_rate = %d, want 16000", req.SamplingRate)
	}
	if req.Response != FormatMP3 {
		t.Errorf("response default = %d, want MP3", req.Response)
	}
	if string(req.ADPCM) != string(audio) {
		t.Errorf("audio = %x, want %x", req.ADPCM, audio)
	}
}

func TestParseRejectsTamperedMAC(t *testing.T) {
	key := []byte("0123456789abcdef")
	body := Sign(key, encodeSpeechRequest(KeywordOKSense, 16000), []byte{1, 2, 3, 4})
	body[len(body)-1] ^= 0xff // corrupt the signature
	if _, err := Parse(key, body); err != ErrBadSig {
		t.Fatalf("err = %v, want ErrBadSig", err)
	}
}

func TestParseRejectsWrongKey(t *testing.T) {
	body := Sign([]byte("0123456789abcdef"), encodeSpeechRequest(KeywordStop, 16000), nil)
	if _, err := Parse([]byte("ffffffffffffffff"), body); err != ErrBadSig {
		t.Fatalf("err = %v, want ErrBadSig", err)
	}
}

func TestParseShortBody(t *testing.T) {
	if _, err := Parse([]byte("0123456789abcdef"), []byte{1, 2, 3}); err != ErrShort {
		t.Fatalf("err = %v, want ErrShort", err)
	}
}

// encodeADPCM is the firmware coder (kitsune/adpcm.c adpcm_coder), used to
// confirm DecodeADPCM inverts exactly what the device produces, including the
// high-nibble-first packing.
func encodeADPCM(pcm []int16) []byte {
	out := make([]byte, 0, len(pcm)/2)
	valpred, index := 0, 0
	step := imaStepTable[0]
	var buf int
	bufferstep := true
	for _, val := range pcm {
		diff := int(val) - valpred
		sign := 0
		if diff < 0 {
			sign = 8
			diff = -diff
		}
		delta := 0
		vpdiff := step >> 3
		if diff >= step {
			delta = 4
			diff -= step
			vpdiff += step
		}
		step >>= 1
		if diff >= step {
			delta |= 2
			diff -= step
			vpdiff += step
		}
		step >>= 1
		if diff >= step {
			delta |= 1
			vpdiff += step
		}
		if sign != 0 {
			valpred -= vpdiff
		} else {
			valpred += vpdiff
		}
		if valpred > 32767 {
			valpred = 32767
		} else if valpred < -32768 {
			valpred = -32768
		}
		delta |= sign
		index += imaIndexTable[delta]
		if index < 0 {
			index = 0
		} else if index > 88 {
			index = 88
		}
		step = imaStepTable[index]

		if bufferstep {
			buf = (delta << 4) & 0xf0
		} else {
			out = append(out, byte((delta&0x0f)|buf))
		}
		bufferstep = !bufferstep
	}
	return out
}

// TestADPCMRoundTrip encodes a sine sweep with the firmware coder, decodes with
// our decoder, and checks the reconstruction tracks. ADPCM is lossy, so this
// asserts a bounded RMS error rather than exact equality; a wrong nibble order
// or table would blow the error up by orders of magnitude.
func TestADPCMRoundTrip(t *testing.T) {
	const n = 4000
	pcm := make([]int16, n)
	for i := range pcm {
		pcm[i] = int16(8000 * math.Sin(float64(i)*2*math.Pi*220/16000))
	}
	enc := encodeADPCM(pcm)
	if len(enc) != n/2 {
		t.Fatalf("encoded %d bytes, want %d", len(enc), n/2)
	}

	dec := DecodeADPCM(enc)
	if len(dec) != n*2 {
		t.Fatalf("decoded %d bytes, want %d", len(dec), n*2)
	}

	var sumSq float64
	for i := 0; i < n; i++ {
		got := int16(binary.LittleEndian.Uint16(dec[i*2:]))
		d := float64(got) - float64(pcm[i])
		sumSq += d * d
	}
	rms := math.Sqrt(sumSq / n)
	if rms > 500 { // signal amplitude is 8000; good ADPCM tracks to well under this
		t.Errorf("ADPCM round-trip RMS error %.1f too high; nibble order or tables likely wrong", rms)
	}
}

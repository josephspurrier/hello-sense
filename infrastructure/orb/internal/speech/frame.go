// Package speech implements the Sense with Voice audio protocol: the device
// streams a wake-word-triggered utterance to POST /v2/upload/audio and plays
// back the MP3 the server returns.
//
// The wire format the device sends (kitsune hlo_audio_tools.c + hlo_pipe.c),
// as one HTTP chunked body:
//
//	[4-byte big-endian length N][N-byte SpeechRequest protobuf][ADPCM audio][20-byte HMAC-SHA1]
//
// The HMAC-SHA1 is over everything before it (the length prefix, the protobuf,
// and the ADPCM), keyed with the Sense's 16-byte AES key. The audio is IMA/DVI
// ADPCM, mono, a single continuous stream started from predictor 0 / index 0.
package speech

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
)

// Keyword is the wake word that triggered the upload (speech.proto Keyword).
type Keyword int32

const (
	KeywordNull    Keyword = 0
	KeywordOKSense Keyword = 1
	KeywordStop    Keyword = 2
	KeywordSnooze  Keyword = 3
	KeywordAlexa   Keyword = 4
	KeywordOkay    Keyword = 5
)

// AudioFormat is the reply codec the device wants (speech.proto AudioFormat).
type AudioFormat int32

const (
	FormatMP3   AudioFormat = 0
	FormatADPCM AudioFormat = 1
	FormatRAW   AudioFormat = 2
)

// Request is the parsed SpeechRequest header plus the raw ADPCM audio.
type Request struct {
	Word         Keyword
	Confidence   int32
	Version      int32
	EQ           int32
	Response     AudioFormat
	SamplingRate int32
	ADPCM        []byte
}

const (
	sigLen    = 20 // HMAC-SHA1
	prefixLen = 4  // big-endian protobuf length
	// The device caps its SpeechRequest well under this; the reference rejected
	// anything over 40 bytes, and a runaway length here would otherwise slice
	// the audio into the protobuf.
	maxPBLen = 40
)

var (
	ErrShort      = errors.New("speech: body too short")
	ErrBadSig     = errors.New("speech: HMAC does not match")
	ErrPBLen      = errors.New("speech: protobuf length out of range")
	ErrBadKeyLen  = errors.New("speech: sense key must be 16 bytes")
	errPBWireType = errors.New("speech: unexpected protobuf wire type")
)

// Parse verifies the HMAC with the Sense key and splits the body into the
// SpeechRequest header and the ADPCM audio.
//
// key is the Sense's 16-byte AES key, used here as the HMAC-SHA1 key exactly as
// the firmware does (get_aes -> hlo_hmac_stream). A body that fails the MAC is
// rejected rather than answered: an unauthenticated caller must not be able to
// make the device speak.
func Parse(key, body []byte) (*Request, error) {
	if len(key) != 16 {
		return nil, ErrBadKeyLen
	}
	if len(body) < prefixLen+sigLen {
		return nil, ErrShort
	}

	signed := body[:len(body)-sigLen]
	sig := body[len(body)-sigLen:]
	mac := hmac.New(sha1.New, key)
	mac.Write(signed)
	if !hmac.Equal(mac.Sum(nil), sig) {
		return nil, ErrBadSig
	}

	n := int(binary.BigEndian.Uint32(signed[:prefixLen]))
	if n < 0 || n > maxPBLen || prefixLen+n > len(signed) {
		return nil, ErrPBLen
	}
	pb := signed[prefixLen : prefixLen+n]
	audio := signed[prefixLen+n:]

	req := &Request{
		// Defaults from speech.proto for fields the device may omit.
		EQ:           1, // SENSE_ONE
		Response:     FormatMP3,
		SamplingRate: 16000,
		ADPCM:        audio,
	}
	if err := parseSpeechRequest(pb, req); err != nil {
		return nil, err
	}
	return req, nil
}

// parseSpeechRequest hand-decodes the six optional scalar fields of
// SpeechRequest. Doing it by hand rather than pulling in a generated type keeps
// the protocol self-contained and is trivial for a message this small: every
// field is a varint (wire type 0).
func parseSpeechRequest(pb []byte, req *Request) error {
	i := 0
	for i < len(pb) {
		tag, m, err := uvarint(pb[i:])
		if err != nil {
			return err
		}
		i += m
		field := tag >> 3
		wire := tag & 7
		if wire != 0 {
			return fmt.Errorf("%w: field %d wire %d", errPBWireType, field, wire)
		}
		v, m, err := uvarint(pb[i:])
		if err != nil {
			return err
		}
		i += m
		switch field {
		case 1:
			req.Word = Keyword(v)
		case 2:
			req.Confidence = int32(v)
		case 3:
			req.Version = int32(v)
		case 4:
			req.EQ = int32(v)
		case 5:
			req.Response = AudioFormat(v)
		case 6:
			req.SamplingRate = int32(v)
		}
	}
	return nil
}

func uvarint(b []byte) (uint64, int, error) {
	var x uint64
	var s uint
	for i, c := range b {
		if i > 9 {
			return 0, 0, errors.New("speech: varint too long")
		}
		if c < 0x80 {
			return x | uint64(c)<<s, i + 1, nil
		}
		x |= uint64(c&0x7f) << s
		s += 7
	}
	return 0, 0, errors.New("speech: truncated varint")
}

// Sign appends the length prefix and HMAC the way the device does. It exists
// for tests and tooling (crafting a valid body), and mirrors Parse exactly.
func Sign(key []byte, pb, adpcm []byte) []byte {
	body := make([]byte, 0, prefixLen+len(pb)+len(adpcm)+sigLen)
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(pb)))
	body = append(body, prefix[:]...)
	body = append(body, pb...)
	body = append(body, adpcm...)
	mac := hmac.New(sha1.New, key)
	mac.Write(body)
	return append(body, mac.Sum(nil)...)
}

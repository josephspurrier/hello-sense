package speech

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Speech-to-text and text-to-speech live in a small sidecar service (Python:
// faster-whisper + piper) rather than in this Go binary, so the heavy ML
// runtimes and their models stay out of the orb image. The sidecar speaks a
// trivial HTTP API:
//
//	POST {STT}  body = WAV (16-bit mono)      -> {"text": "..."}
//	POST {TTS}  body = {"text": "..."}         -> MP3 bytes
//
// When no sidecar is configured (or it errors), the endpoint still closes the
// loop by playing a canned MP3, so the device always gets a valid reply.

//go:embed assets/fallback.mp3
var fallbackMP3 []byte

//go:embed assets/didnt_catch.mp3
var didntCatchMP3 []byte

//go:embed assets/silence.mp3
var silenceMP3 []byte

// FallbackMP3 is spoken when the assistant cannot answer (no sidecar, or an
// unrecognized request). DidntCatchMP3 is for empty transcripts. SilenceMP3 is
// half a second of encoder silence used to pad a streamed reply out to its
// declared Content-Length.
func FallbackMP3() []byte   { return fallbackMP3 }
func DidntCatchMP3() []byte { return didntCatchMP3 }
func SilenceMP3() []byte    { return silenceMP3 }

// Synth is the client for the STT/TTS sidecar. A zero Synth (no URLs) is valid
// and simply has no STT or TTS, which the caller handles by falling back to the
// canned audio.
type Synth struct {
	STTURL    string
	TTSURL    string
	StreamURL string // optional /tts_stream endpoint; empty disables streaming
	HTTP      *http.Client
}

func NewSynth(sttURL, ttsURL, streamURL string) *Synth {
	return &Synth{STTURL: sttURL, TTSURL: ttsURL, StreamURL: streamURL,
		HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Available reports whether both halves of the pipeline are configured.
func (s *Synth) Available() bool { return s != nil && s.STTURL != "" && s.TTSURL != "" }

// StreamAvailable reports whether the streamed-synthesis endpoint is configured.
func (s *Synth) StreamAvailable() bool { return s.Available() && s.StreamURL != "" }

// Transcribe sends WAV audio to the sidecar and returns the recognized text.
// An empty string with no error means the recognizer heard nothing.
func (s *Synth) Transcribe(ctx context.Context, wav []byte) (string, error) {
	if s == nil || s.STTURL == "" {
		return "", fmt.Errorf("speech: no STT configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.STTURL, bytes.NewReader(wav))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "audio/wav")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("speech: stt status %d", resp.StatusCode)
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Text, nil
}

// SynthesizeStream asks the sidecar to synthesize text fragment by fragment.
// It returns the response body (MP3 bytes arriving as they are synthesized)
// and the sidecar's estimated total size, which is an upper bound suitable
// for a Content-Length declaration. The caller owns closing the body.
func (s *Synth) SynthesizeStream(ctx context.Context, text string) (io.ReadCloser, int, error) {
	if s == nil || s.StreamURL == "" {
		return nil, 0, fmt.Errorf("speech: no streaming TTS configured")
	}
	payload, _ := json.Marshal(map[string]string{"text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.StreamURL, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("speech: tts stream status %d", resp.StatusCode)
	}
	est, err := strconv.Atoi(resp.Header.Get("X-Estimated-Bytes"))
	if err != nil || est <= 0 {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("speech: tts stream missing estimate")
	}
	return resp.Body, est, nil
}

// CopyPadded relays src to dst up to exactly total bytes, flushing after every
// write so audio leaves as it is synthesized, then pads the remainder with
// silence frames. src running past total is truncated (the estimate is an
// upper bound, so this indicates a bad estimate, not normal operation); the
// return reports how many real audio bytes were written and whether src was
// truncated. flush may be nil.
func CopyPadded(dst io.Writer, src io.Reader, total int, flush func()) (int, bool, error) {
	if flush == nil {
		flush = func() {}
	}
	written := 0
	truncated := false
	buf := make([]byte, 4096)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if written+n > total {
				chunk = chunk[:total-written]
				truncated = true
			}
			if len(chunk) > 0 {
				if _, werr := dst.Write(chunk); werr != nil {
					return written, truncated, werr
				}
				written += len(chunk)
				flush()
			}
			if truncated {
				return written, truncated, nil
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			// The device already has the declared length promised to it; pad
			// out what we can rather than cutting the response short.
			break
		}
	}
	audio := written
	for written < total {
		pad := silenceMP3
		if rem := total - written; rem < len(pad) {
			pad = pad[:rem]
		}
		if _, err := dst.Write(pad); err != nil {
			return audio, truncated, err
		}
		written += len(pad)
		flush()
	}
	return audio, truncated, nil
}

// Synthesize turns reply text into MP3 bytes the device can play.
func (s *Synth) Synthesize(ctx context.Context, text string) ([]byte, error) {
	if s == nil || s.TTSURL == "" {
		return nil, fmt.Errorf("speech: no TTS configured")
	}
	payload, _ := json.Marshal(map[string]string{"text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.TTSURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("speech: tts status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

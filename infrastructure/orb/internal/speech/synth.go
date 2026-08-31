package speech

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// FallbackMP3 is spoken when the assistant cannot answer (no sidecar, or an
// unrecognized request). DidntCatchMP3 is for empty transcripts.
func FallbackMP3() []byte   { return fallbackMP3 }
func DidntCatchMP3() []byte { return didntCatchMP3 }

// Synth is the client for the STT/TTS sidecar. A zero Synth (no URLs) is valid
// and simply has no STT or TTS, which the caller handles by falling back to the
// canned audio.
type Synth struct {
	STTURL string
	TTSURL string
	HTTP   *http.Client
}

func NewSynth(sttURL, ttsURL string) *Synth {
	return &Synth{STTURL: sttURL, TTSURL: ttsURL,
		HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Available reports whether both halves of the pipeline are configured.
func (s *Synth) Available() bool { return s != nil && s.STTURL != "" && s.TTSURL != "" }

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

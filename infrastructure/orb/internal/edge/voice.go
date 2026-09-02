package edge

import (
	"os"
	"path/filepath"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/josephspurrier/hello-orb/orb/internal/messeji"
	"github.com/josephspurrier/hello-orb/orb/internal/roomstate"
	"github.com/josephspurrier/hello-orb/orb/internal/sense"
	"github.com/josephspurrier/hello-orb/orb/internal/speech"
)

// maxAudioBody caps an utterance upload. A spoken command is a few seconds of
// ADPCM (~2 KB/s), so this is generous while still bounding a stuck stream.
const maxAudioBody = 1 << 20

// voicePing answers the device's /v2/ping keepalive. The firmware has this
// path #if 0'd out today, but answering it costs nothing and future-proofs the
// keepalive.
func (h *Handler) voicePing(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// uploadAudio is the Sense with Voice endpoint: the device streams a
// wake-word utterance, and the body it gets back is the MP3 it speaks.
//
// The loop always closes with valid audio. Anything that goes wrong short of a
// bad signature, no recognizer, an empty transcript, an unhandled request,
// answers with canned speech rather than an error, because the device turns a
// non-2xx into a failed-playback and the person just hears nothing.
func (h *Handler) uploadAudio(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	deviceID := r.Header.Get(senseIDHeader)
	if deviceID == "" {
		http.Error(w, "missing sense id", http.StatusBadRequest)
		return
	}

	// The Sense's key authenticates the upload and its account owns the data a
	// reply is built from. An unpaired or unknown device cannot be answered.
	dev, err := h.store.SenseByID(ctx, deviceID)
	if err != nil {
		h.fail(w, "voice: unknown sense", fmt.Errorf("%s: %w", deviceID, err), http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxAudioBody))
	if err != nil {
		h.fail(w, "voice: read body", err, http.StatusBadRequest)
		return
	}
	req, err := speech.Parse(dev.AESKey, body)
	if err != nil {
		h.fail(w, "voice: parse", fmt.Errorf("%s: %w", deviceID, err), http.StatusUnauthorized)
		return
	}

	// STOP and SNOOZE are handled by the device itself (alarm control); the
	// server just acknowledges with an empty 200, no speech, matching supichi.
	if req.Word == speech.KeywordStop || req.Word == speech.KeywordSnooze {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Muted: acknowledge without transcribing or answering. The always-on
	// wake word still fires the LED on-device (nothing server-side can stop
	// that on this hardware), but a muted Sense neither speaks back nor has
	// its captured audio run through speech-to-text. The empty 200 is the same
	// "handled, no speech" the device expects from STOP/SNOOZE.
	if _, _, muted, ok, err := h.store.VoicePushInfo(ctx, deviceID); err == nil && ok && muted {
		w.WriteHeader(http.StatusOK)
		return
	}

	// No recognizer wired up: close the loop with the canned reply so the
	// device still speaks instead of erroring.
	if !h.Synth.Available() {
		h.writeMP3(w, speech.FallbackMP3())
		return
	}

	pcm := speech.DecodeADPCM(req.ADPCM)
	wav := speech.PCMToWAV(pcm, int(req.SamplingRate))
	// Wake-audio capture: when the export dir is enabled, keep a copy of each
	// wake upload. This is how real-device wake-word training data is collected
	// (the device mic hears what no offline recording reproduces). Off unless
	// ORB_EXPORT_DIR is set, same switch as the file-export landing zone.
	if h.ExportDir != "" {
		name := fmt.Sprintf("wake_%s_%d.wav", deviceID, time.Now().UnixMilli())
		if werr := os.WriteFile(filepath.Join(h.ExportDir, name), wav, 0o644); werr == nil {
			h.log.Warn("captured wake audio", "file", name, "bytes", len(wav))
		}
	}
	text, err := h.Synth.Transcribe(ctx, wav)
	if err != nil {
		h.log.Warn("voice transcribe failed", "device", deviceID, "err", err)
		h.writeMP3(w, speech.FallbackMP3())
		return
	}
	if text == "" {
		h.writeMP3(w, speech.DidntCatchMP3())
		return
	}
	h.log.Info("voice transcript", "device", deviceID, "account", dev.AccountID, "text", text)

	reply, ok := h.voiceReply(ctx, dev.AccountID, speech.Classify(text))
	if !ok {
		h.writeMP3(w, speech.FallbackMP3())
		return
	}

	// Streamed synthesis: the device plays the MP3 progressively, so sending
	// fragments as they are synthesized moves first audio up by roughly a
	// second. Falls back to whole-reply synthesis when not configured or when
	// the stream cannot start.
	if h.Synth.StreamAvailable() {
		if h.writeStreamedMP3(w, ctx, deviceID, reply) {
			return
		}
	}

	mp3, err := h.Synth.Synthesize(ctx, reply)
	if err != nil {
		h.log.Warn("voice synth failed", "device", deviceID, "err", err)
		h.writeMP3(w, speech.FallbackMP3())
		return
	}
	h.log.Info("voice reply", "device", deviceID, "reply", reply)
	h.writeMP3(w, mp3)
}

// writeStreamedMP3 relays fragment-by-fragment synthesis to the device. The
// Content-Length is the sidecar's upper-bound estimate and the tail is padded
// with silence, because the device needs the length before the first byte but
// plays bytes as they arrive. Returns false only when nothing has been
// written yet and the caller can still fall back; once the header is out this
// path owns the response.
func (h *Handler) writeStreamedMP3(w http.ResponseWriter, ctx context.Context, deviceID, reply string) bool {
	body, est, err := h.Synth.SynthesizeStream(ctx, reply)
	if err != nil {
		h.log.Warn("voice stream synth unavailable", "device", deviceID, "err", err)
		return false
	}
	defer body.Close()

	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Content-Length", strconv.Itoa(est))
	w.WriteHeader(http.StatusOK)

	flush := func() {}
	if f, ok := w.(http.Flusher); ok {
		flush = f.Flush
	}
	start := time.Now()
	audio, truncated, err := speech.CopyPadded(w, body, est, flush)
	if err != nil {
		h.log.Warn("voice stream write", "device", deviceID, "err", err)
		return true
	}
	if truncated {
		h.log.Warn("voice stream truncated", "device", deviceID, "estimate", est)
	}
	h.log.Info("voice reply", "device", deviceID, "reply", reply,
		"streamed", true, "audio_bytes", audio, "declared", est,
		"ms", time.Since(start).Milliseconds())
	return true
}

// voiceState is the last mute/volume orb delivered to a device, cached so a
// command is only re-sent when the desired state actually drifts.
type voiceState struct {
	enabled bool
	volume  uint32
}

// pushVoiceState delivers a voice-control (mute) or volume command over the
// messeji long-poll when the desired state has drifted from what was last sent,
// so an in-app change takes effect on the next poll (~10s) instead of never.
// Returns true when it wrote a response (so the caller stops).
//
// Mute maps to the firmware's disable_voice, not to volume 0: a muted Sense
// ignores trigger words entirely (no upload, no speech) and, importantly, does
// NOT light its wake LED, because the firmware draws the wake glow only while
// voice is enabled. SET_VOLUME alone would only silence the speaker. Enable
// governs listening and the LED, so it is sent ahead of any volume change;
// volume matters only while enabled.
func (h *Handler) pushVoiceState(ctx context.Context, w http.ResponseWriter, deviceID string) bool {
	key, volume, muted, ok, err := h.store.VoicePushInfo(ctx, deviceID)
	if err != nil {
		h.log.Warn("voice state lookup", "device", deviceID, "err", err)
		return false
	}
	if !ok {
		return false
	}
	want := voiceState{enabled: !muted, volume: volume}

	// Unseen this process: the device boots voice-enabled and near-silent, so
	// assume enabled and force a first volume push with an impossible sentinel.
	prev := voiceState{enabled: true, volume: ^uint32(0)}
	if v, seen := h.volumePushed.Load(deviceID); seen {
		prev = v.(voiceState)
	}

	// message_id is the unix-nano clock: unique per command and monotonic, so a
	// later push always looks newer to the device's ack queue.
	send := func(batch []byte, next voiceState, key0, val string) bool {
		signed, serr := sense.Sign(key, batch)
		if serr != nil {
			h.log.Error("sign voice state", "device", deviceID, "err", serr)
			return false
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(signed)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(signed)
		h.volumePushed.Store(deviceID, next)
		h.log.Info("pushed voice state", "device", deviceID, key0, val)
		return true
	}

	if prev.enabled != want.enabled {
		return send(messeji.VoiceControlBatch(want.enabled, time.Now().UnixNano()),
			voiceState{enabled: want.enabled, volume: prev.volume},
			"voice_enabled", strconv.FormatBool(want.enabled))
	}
	if want.enabled && prev.volume != want.volume {
		return send(messeji.VolumeBatch(want.volume, time.Now().UnixNano()),
			voiceState{enabled: want.enabled, volume: want.volume},
			"volume", strconv.FormatUint(uint64(want.volume), 10))
	}
	return false
}

func (h *Handler) writeMP3(w http.ResponseWriter, mp3 []byte) {
	// Content-Length explicitly, BEFORE WriteHeader: the device reads the
	// response body straight into its MP3 decoder, so it needs a framed length,
	// not chunked transfer-encoding (which Go would use if the header went out
	// before the size was known). A chunked reply feeds chunk-size markers into
	// libmad as if they were audio.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(mp3)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(mp3)
}

// voiceReply turns a classified request into the sentence to speak. ok=false
// means the request was understood as words but not as something orb can
// answer, so the caller plays the canned "can't help" reply.
//
// The read-only intents (time, room conditions, sleep score) answer from data
// orb already holds. The action intents (alarms, sounds) are acknowledged but
// not yet wired to their effects; that is called out in each reply so the
// person is not misled into thinking an alarm was set.
func (h *Handler) voiceReply(ctx context.Context, accountID int64, m speech.Match) (string, bool) {
	switch m.Intent {
	case speech.IntentTime:
		loc := h.accountLocation(ctx, accountID)
		return "It's " + time.Now().In(loc).Format("3:04 PM") + ".", true

	case speech.IntentTemperature, speech.IntentHumidity, speech.IntentAirQuality, speech.IntentLight:
		s, err := h.store.LatestSample(ctx, accountID)
		if err != nil || s == nil {
			return "I don't have a recent reading from your Sense.", true
		}
		switch m.Intent {
		case speech.IntentTemperature:
			c := roomstate.CalibratedTemperature(s.Temperature)
			f := c*9/5 + 32
			return fmt.Sprintf("It's %.0f degrees.", f), true
		case speech.IntentHumidity:
			return fmt.Sprintf("The humidity is %.0f percent.",
				roomstate.CalibratedHumidity(s.Temperature, s.Humidity)), true
		case speech.IntentAirQuality:
			return fmt.Sprintf("Air quality is %.0f micrograms per cubic meter.",
				roomstate.CalibratedParticulates(s.AirQualityRaw, s.DustOffset)), true
		case speech.IntentLight:
			return fmt.Sprintf("The light level is %.0f lux.",
				roomstate.CalibratedLux(s.Light)), true
		}

	case speech.IntentSleepScore:
		score, ok, err := h.store.LastSleepScore(ctx, accountID)
		if err != nil || !ok {
			return "I don't have a sleep score for you yet.", true
		}
		return fmt.Sprintf("Your last sleep score was %d.", score), true

	case speech.IntentAlarmSet:
		if m.AlarmHour < 0 {
			return "I didn't catch what time to set the alarm for.", true
		}
		t := time.Date(2000, 1, 1, m.AlarmHour, m.AlarmMin, 0, 0, time.UTC)
		return "Setting alarms by voice isn't supported yet, but you asked for " +
			t.Format("3:04 PM") + ".", true

	case speech.IntentAlarmQuery:
		return "Checking alarms by voice isn't supported yet.", true
	case speech.IntentAlarmCancel:
		return "Canceling alarms by voice isn't supported yet.", true
	case speech.IntentSleepSoundPlay, speech.IntentSleepSoundStop:
		return "Sleep sounds by voice aren't supported yet.", true
	}
	return "", false
}

// accountLocation resolves the account's current timezone, falling back to UTC
// so a spoken time is at least well-formed when no zone is on file.
func (h *Handler) accountLocation(ctx context.Context, accountID int64) *time.Location {
	if loc, err := h.store.AccountLocation(ctx, accountID); err == nil && loc != nil {
		return loc
	}
	return time.UTC
}

// keywordFeatures receives the wake-word net's own input: on every detection
// the device uploads a SimpleMatrix protobuf holding the ~3 s int8 mel-feature
// circular buffer around the keyword (audio_features_upload_task.c, rate
// limited to 2 per 5 min). Stock backends threw this away; saved raw when the
// export dir is enabled, it is real-device wake-word training data in exactly
// the representation the model trains on. Decode offline with onsei's
// matrix_pb2. Always 204: the device only needs a 2xx to stop waiting.
func (h *Handler) keywordFeatures(w http.ResponseWriter, r *http.Request) {
	dev, payload, err := h.authByHeader(r)
	if err != nil {
		h.fail(w, "keyword features auth", err, http.StatusUnauthorized)
		return
	}
	if h.ExportDir != "" {
		name := fmt.Sprintf("kwfeats_%s_%d.pb", dev.DeviceID, time.Now().UnixMilli())
		if werr := os.WriteFile(filepath.Join(h.ExportDir, name), payload, 0o644); werr == nil {
			h.log.Warn("captured keyword features", "file", name, "bytes", len(payload))
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

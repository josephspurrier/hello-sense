package edge

import (
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

	// No recognizer wired up: close the loop with the canned reply so the
	// device still speaks instead of erroring.
	if !h.Synth.Available() {
		h.writeMP3(w, speech.FallbackMP3())
		return
	}

	pcm := speech.DecodeADPCM(req.ADPCM)
	wav := speech.PCMToWAV(pcm, int(req.SamplingRate))
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

	mp3, err := h.Synth.Synthesize(ctx, reply)
	if err != nil {
		h.log.Warn("voice synth failed", "device", deviceID, "err", err)
		h.writeMP3(w, speech.FallbackMP3())
		return
	}
	h.log.Info("voice reply", "device", deviceID, "reply", reply)
	h.writeMP3(w, mp3)
}

// pushVoiceVolume delivers a signed SET_VOLUME over the messeji long-poll.
// Returns true when it wrote a response (so the caller marks the device done
// and stops), false to fall through to the normal wait (not a voice unit, no
// key, or an encode error, none of which should hold up the poll).
func (h *Handler) pushVoiceVolume(ctx context.Context, w http.ResponseWriter, deviceID string) bool {
	key, volume, ok, err := h.store.VoicePushInfo(ctx, deviceID)
	if err != nil {
		h.log.Warn("voice volume lookup", "device", deviceID, "err", err)
		return false
	}
	if !ok {
		return false
	}
	// message_id is the unix-nano clock: unique enough for the device's ack
	// queue, and monotonic so a later push always looks newer.
	batch := messeji.VolumeBatch(volume, time.Now().UnixNano())
	signed, err := sense.Sign(key, batch)
	if err != nil {
		h.log.Error("sign voice volume", "device", deviceID, "err", err)
		return false
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(signed)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(signed)
	h.log.Info("pushed voice volume", "device", deviceID, "volume", volume)
	return true
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

package api

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/josephspurrier/hello-orb/orb/internal/messeji"
	"github.com/josephspurrier/hello-orb/orb/internal/sense"
)

// Playing sleep sounds on the Sense.
//
// The play button POSTs /v2/sleep_sounds/play, which suripu turned into a
// messeji PlayAudio command; the device collects it on its long-poll and plays
// the file from its own SD card. orb's long-poll (edge.receive) and its
// command queue (device_messages) already existed for the voice unit's volume
// push, so play and stop are: encode the command, sign it with the device key,
// queue it. The device picks it up within half a second.
//
// The status the app polls afterwards is not an echo of what we sent. The
// firmware reports its audio state (playing, file, duration) in a SenseState
// upload whenever playback starts or stops, and orb records it verbatim
// (edge.senseState). Reading it back is what makes the play button flip to a
// stop button only when the device actually started playing, which is also
// exactly how suripu did it (SenseStateDynamoDB there, the senses.state blob
// here).

// sleepSoundPlayRequest is the app's play body. The JSON keys are "sound" and
// "duration" even though they carry ids; that is SENSleepSoundRequestPlay's
// naming and suripu's PlayRequest agrees. order is the app's ordering value
// (epoch millis at tap time), passed through to the device.
type sleepSoundPlayRequest struct {
	Sound         *int   `json:"sound"`
	Duration      *int   `json:"duration"`
	Order         *int64 `json:"order"`
	VolumePercent *int   `json:"volume_percent"`
}

type sleepSoundStopRequest struct {
	Order *int64 `json:"order"`
}

// devicePath is the file the Sense plays for this sound, from the same
// `file_info` rows the names came from. Both catalogue generations map names
// to the same ST files (the 2016 `file_info` and 2017 `file_info_one_five`
// agree on every one), so this holds for the voice and no-voice cards alike.
func (s SleepSound) devicePath() string {
	return "/SLPTONES/" + strings.TrimSuffix(s.file, ".mp3") + ".RAW"
}

// senseVolumePercent converts the app's perceived-loudness percent into the
// linear scaling factor the firmware applies to its 60 dB ceiling. suripu's
// formula (SleepSoundsResource.convertToSenseVolumePercent): every halving of
// perceived loudness is about 10 dB down, so 100 -> 100, 50 -> 83, 25 -> 67.
func senseVolumePercent(volumePercent int) uint32 {
	const maxDecibels = 60.0
	if volumePercent <= 1 {
		return 0
	}
	decibels := maxDecibels + 33.22*math.Log10(float64(volumePercent)/100.0)
	return uint32(math.Round(decibels / maxDecibels * 100))
}

func (h *Handler) invalidRequest(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, errorBody{Code: http.StatusBadRequest, Message: msg})
}

// queueSoundCommand signs a messeji batch for the account's Sense and queues
// it on the long-poll. Shared by play and stop; the error strings are
// suripu's, since the app shows them to the person holding the phone.
func (h *Handler) queueSoundCommand(w http.ResponseWriter, r *http.Request, accountID int64, batch func(messageID int64) []byte) {
	deviceID, err := h.store.ActiveSenseID(r.Context(), accountID)
	if err != nil {
		h.log.Error("sleep sound device", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if deviceID == "" {
		h.invalidRequest(w, "no device pair found")
		return
	}
	key, ok, err := h.store.SenseKey(r.Context(), deviceID)
	if err != nil || !ok {
		h.log.Error("sleep sound key", "device", deviceID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// message_id is the unix-nano clock, same as the volume push: unique
	// enough for the device's ack queue, monotonic so newer always wins.
	signed, err := sense.Sign(key, batch(time.Now().UnixNano()))
	if err != nil {
		h.log.Error("sign sleep sound command", "device", deviceID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if err := h.store.QueueDeviceMessage(r.Context(), deviceID, signed); err != nil {
		h.log.Error("queue sleep sound command", "device", deviceID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// 202 with an empty body, exactly suripu's reply: the command is queued,
	// not yet playing. The app learns the outcome from the status poll.
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) postSleepSoundsPlay(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req sleepSoundPlayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.invalidRequest(w, "invalid request body")
		return
	}
	if req.Sound == nil || req.Duration == nil || req.Order == nil ||
		req.VolumePercent == nil || *req.VolumePercent < 0 || *req.VolumePercent > 100 {
		h.invalidRequest(w, "invalid request body")
		return
	}
	var duration *SleepSoundDuration
	for i := range sleepDurations {
		if sleepDurations[i].ID == *req.Duration {
			duration = &sleepDurations[i]
			break
		}
	}
	if duration == nil {
		h.invalidRequest(w, "invalid duration id")
		return
	}
	var sound *SleepSound
	for i := range sleepSounds {
		if sleepSounds[i].ID == *req.Sound {
			sound = &sleepSounds[i]
			break
		}
	}
	if sound == nil {
		h.invalidRequest(w, "invalid sound id")
		return
	}

	volume := senseVolumePercent(*req.VolumePercent)
	h.queueSoundCommand(w, r, accountID, func(messageID int64) []byte {
		return messeji.PlayAudioBatch(sound.devicePath(), volume, uint32(duration.secs), *req.Order, messageID)
	})
	h.log.Info("sleep sound play queued",
		"account", accountID, "sound", sound.Name, "duration", duration.Name,
		"volume_percent", *req.VolumePercent, "sense_volume", volume)
}

func (h *Handler) postSleepSoundsStop(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req sleepSoundStopRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Order == nil {
		h.invalidRequest(w, "invalid request body")
		return
	}
	h.queueSoundCommand(w, r, accountID, func(messageID int64) []byte {
		return messeji.StopAudioBatch(*req.Order, messageID)
	})
	h.log.Info("sleep sound stop queued", "account", accountID)
}

// SleepSoundsStatus is the shape of GET /v2/sleep_sounds/status: whether the
// device says it is playing, and what. The app polls this constantly, and it
// is what flips the play button to a stop button.
type SleepSoundsStatus struct {
	Playing       bool                `json:"playing"`
	Sound         *SleepSound         `json:"sound"`
	Duration      *SleepSoundDuration `json:"duration"`
	VolumePercent *int                `json:"volume_percent"`
}

// senseStateBlob is the slice of the recorded SenseState (protojson, see
// edge.senseState) that the status endpoint reads.
type senseStateBlob struct {
	AudioState *struct {
		PlayingAudio    bool   `json:"playingAudio"`
		DurationSeconds int    `json:"durationSeconds"`
		FilePath        string `json:"filePath"`
	} `json:"audioState"`
}

func (h *Handler) getSleepSoundsStatus(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, h.sleepSoundsStatus(r, accountID))
}

// sleepSoundsStatus builds the status from the device's own last report.
func (h *Handler) sleepSoundsStatus(r *http.Request, accountID int64) SleepSoundsStatus {
	notPlaying := SleepSoundsStatus{Playing: false}

	deviceID, err := h.store.ActiveSenseID(r.Context(), accountID)
	if err != nil || deviceID == "" {
		return notPlaying
	}
	blob, ok, err := h.store.SenseState(r.Context(), deviceID)
	if err != nil || !ok {
		return notPlaying
	}
	return statusFromSenseState(r, blob)
}

// statusFromSenseState maps a recorded state blob onto the app's status.
// Anything unrecognized (no audio state, a file that is not a sleep tone, a
// duration we never offered) reads as not playing, which is suripu's rule
// too: a status the app cannot act on is worse than a stopped one.
func statusFromSenseState(r *http.Request, blob []byte) SleepSoundsStatus {
	notPlaying := SleepSoundsStatus{Playing: false}

	var st senseStateBlob
	if json.Unmarshal(blob, &st) != nil || st.AudioState == nil || !st.AudioState.PlayingAudio {
		return notPlaying
	}

	var sound *SleepSound
	for _, s := range sleepSoundsWithPreviews(r) {
		if strings.EqualFold(s.devicePath(), st.AudioState.FilePath) {
			sound = &s
			break
		}
	}
	if sound == nil {
		// Playing something that is not a sleep tone: an alarm, the voice
		// unit's speech. Not this endpoint's business.
		return notPlaying
	}
	// The 1.9.2 firmware plays "until stopped" as an internal -1, which its
	// nanopb encoder sends as uint32 4294967295; later firmware omits the
	// field, which parses here as 0. Both mean Indefinitely (secs 0).
	reported := st.AudioState.DurationSeconds
	if reported >= math.MaxInt32 {
		reported = 0
	}
	var duration *SleepSoundDuration
	for i := range sleepDurations {
		if sleepDurations[i].secs == reported {
			duration = &sleepDurations[i]
			break
		}
	}
	if duration == nil {
		return notPlaying
	}
	return SleepSoundsStatus{Playing: true, Sound: sound, Duration: duration}
}

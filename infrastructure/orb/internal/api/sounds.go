package api

import (
	"net/http"
	"path"
	"strings"

	"github.com/josephspurrier/hello-orb/orb/internal/alarm"
	"github.com/josephspurrier/hello-orb/orb/internal/api/soundpreview"
)

// Alarm ringtones and sleep sounds.
//
// # The audio was gone, and now it is not
//
// Both of these carry a URL field for the phone to play a preview, which used
// to point at Hello's `hello-audio` S3 bucket. That bucket is empty, and a
// content-hash sweep of every blob in all 135 repositories found none of the
// sleep tones (24,631 blobs, zero matches, 2026-08-26; the write-up is in
// knowledgebase/GOING-PUBLIC.md). suripu's own previews were broken anyway: it
// signs the ringtone URLs against `http://localstack:4566/...`, a
// Docker-internal hostname the phone cannot resolve. So previews had not
// worked here at any point, and orb shipped with the URL fields omitted, which
// the app tolerates (`dict[@"url"]` and
// `SENObjectOfClass(dictionary[@"preview_url"], ...)` both yield nil).
//
// The audio was recovered from a Sense's own SD card on 2026-08-31, exactly as
// planned: all twelve SLPTONES files verify byte-exact against the SHA1s in
// the `file_info_one_five` table, and the fifteen RINGTONE files came off the
// same card. The previews are re-encoded to mp3, embedded in the binary by the
// `soundpreview` package, and served the way `insightart` serves the card
// banners. The URL fields below are filled in from the request origin; the
// lists themselves did not change.
//
// The preview is still only a preview. The id is what the app writes onto the
// alarm, and the Sense rings the tone from its own SD card by that id. To keep
// the preview honest, the ringtone's file name is derived from
// alarm.SoundPath, the same mapping the sync response uses, so the phone can
// only ever preview the file the device will play.

// soundPreviewPath is where orb serves the preview audio. Like the insight
// card art, the app derives nothing from the shape: it plays the URLs we send.
const soundPreviewPath = "/v1/sounds/previews/"

// AlarmSound is one ringtone in the alarm tone picker.
type AlarmSound struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	// URL is filled per-request from the origin the phone used to reach us,
	// and omitted if the preview file is somehow absent. `omitempty` rather
	// than a null, because a null would be a third state the app was never
	// sent while the audio was missing.
	URL string `json:"url,omitempty"`
}

// alarmSounds is the reference's list, in the reference's order.
//
// Hardcoded there too, in AlarmGroupsResource.getAlarmSounds, with the comment
// "this is the order in which they appear in the app". The order is the
// contract: the app renders the list as given, so reordering renames every
// tone from the user's point of view. The ids are NOT sequential in display
// order (Dusk is 5, Pulse is 4) and that is the reference's doing, not a
// transcription slip.
var alarmSounds = []AlarmSound{
	{ID: 5, Name: "Dusk"},
	{ID: 4, Name: "Pulse"},
	{ID: 6, Name: "Lilt"},
	{ID: 7, Name: "Bounce"},
	{ID: 8, Name: "Celebration"},
	{ID: 9, Name: "Milky Way"},
	{ID: 10, Name: "Waves"},
	{ID: 11, Name: "Lights"},
	{ID: 12, Name: "Echo"},
	{ID: 13, Name: "Drops"},
	{ID: 14, Name: "Twinkle"},
	{ID: 15, Name: "Silver"},
	{ID: 16, Name: "Highlights"},
	{ID: 17, Name: "Ripple"},
	{ID: 18, Name: "Sway"},
}

// ringtonePreviewFile maps a ringtone id to its preview's file name, through
// the same mapping that decides what the device rings. /RINGTONE/DIG002.raw
// becomes DIG002.mp3.
func ringtonePreviewFile(soundID int) string {
	return strings.TrimSuffix(path.Base(alarm.SoundPath(soundID)), ".raw") + ".mp3"
}

func (h *Handler) getAlarmSounds(w http.ResponseWriter, r *http.Request) {
	if _, ok := AccountFrom(r); !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	base := insightImageOrigin(r) + soundPreviewPath
	out := make([]AlarmSound, len(alarmSounds))
	for i, s := range alarmSounds {
		out[i] = s
		if name := ringtonePreviewFile(s.ID); soundpreview.Has(name) {
			out[i].URL = base + name
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// SleepSound is one sleep tone.
type SleepSound struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	PreviewURL string `json:"preview_url,omitempty"`
	// file is the preview's name in the soundpreview package: the on-device
	// SLPTONES stem, from the same `file_info_one_five` rows the names and
	// SHA1s came from. Unexported so it never leaks into the JSON.
	file string
}

// SleepSoundDuration is one entry in the "play for how long" picker.
type SleepSoundDuration struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// The nesting is the reference's and the app depends on it: `availableSounds`
// wraps a `sounds` array and carries its own `state`, rather than being the
// array itself.
type sleepSoundList struct {
	Sounds []SleepSound `json:"sounds"`
	State  string       `json:"state"`
}

type sleepDurationList struct {
	Durations []SleepSoundDuration `json:"durations"`
}

// CombinedSleepSoundState is GET /v2/sleep_sounds/combined_state.
//
// Three things the app would otherwise fetch separately. `status` is the same
// payload /v2/sleep_sounds/status already serves, so it is built from the same
// function rather than duplicated: two answers to "is a sound playing" that can
// disagree is exactly the kind of split this consolidation exists to remove.
type CombinedSleepSoundState struct {
	AvailableDurations sleepDurationList `json:"availableDurations"`
	AvailableSounds    sleepSoundList    `json:"availableSounds"`
	Status             SleepSoundsStatus `json:"status"`
}

// sleepSounds matches the `file_info` rows, which are the authority: each name
// maps to a real file on the device's SD card. The ids are the sort_key values
// the app orders by, and they skip 8 exactly as the source does.
var sleepSounds = []SleepSound{
	{ID: 1, Name: "Aura", file: "ST010.mp3"},
	{ID: 2, Name: "Nocturne", file: "ST012.mp3"},
	{ID: 3, Name: "Morpheus", file: "ST009.mp3"},
	{ID: 4, Name: "Horizon", file: "ST011.mp3"},
	{ID: 5, Name: "Cosmos", file: "ST002.mp3"},
	{ID: 6, Name: "Autumn Wind", file: "ST003.mp3"},
	{ID: 7, Name: "Fireside", file: "ST004.mp3"},
	{ID: 8, Name: "Rainfall", file: "ST006.mp3"},
	{ID: 9, Name: "Forest Creek", file: "ST008.mp3"},
	{ID: 10, Name: "Brown Noise", file: "ST001.mp3"},
	{ID: 11, Name: "White Noise", file: "ST007.mp3"},
}

// sleepSoundsWithPreviews is the served list: the static entries with
// PreviewURL filled in from the origin the phone reached us on, for the same
// reason the insight card art does it that way (the app hands the string to
// `[NSURL URLWithString:]` with no base, and orb cannot know its own address).
func sleepSoundsWithPreviews(r *http.Request) []SleepSound {
	base := insightImageOrigin(r) + soundPreviewPath
	out := make([]SleepSound, len(sleepSounds))
	for i, s := range sleepSounds {
		out[i] = s
		if soundpreview.Has(s.file) {
			out[i].PreviewURL = base + s.file
		}
	}
	return out
}

// sleepDurations is the reference's fixed set. "Indefinitely" is a real option
// and not a placeholder: it plays until stopped.
var sleepDurations = []SleepSoundDuration{
	{ID: 1, Name: "10 Minutes"},
	{ID: 2, Name: "30 Minutes"},
	{ID: 3, Name: "1 Hour"},
	{ID: 4, Name: "2 Hours"},
	{ID: 5, Name: "3 Hours"},
	{ID: 6, Name: "Indefinitely"},
}

// sleepSoundsState is "OK" whenever the list can be served.
//
// The reference has other values for a device that is too old or has no SD
// card, and neither applies: this Sense is on 1.9.2 and reports a card on every
// file manifest. Sending "OK" unconditionally is therefore correct today and
// would be wrong on a fleet.
const sleepSoundsState = "OK"

func (h *Handler) getCombinedSleepSoundState(w http.ResponseWriter, r *http.Request) {
	if _, ok := AccountFrom(r); !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	writeJSON(w, http.StatusOK, CombinedSleepSoundState{
		AvailableDurations: sleepDurationList{Durations: sleepDurations},
		AvailableSounds:    sleepSoundList{Sounds: sleepSoundsWithPreviews(r), State: sleepSoundsState},
		// The SAME value /v2/sleep_sounds/status serves, not a second copy of
		// the same literal. Two answers to "is a sound playing" that can drift
		// apart is the class of split this consolidation exists to remove, and
		// this endpoint exists precisely so the app can ask both at once.
		Status: sleepSoundsStatus(),
	})
}

package api

import "net/http"

// Alarm ringtones and sleep sounds.
//
// # The audio is gone, and these endpoints ship without it
//
// Both of these carry a URL field pointing at Hello's `hello-audio` S3 bucket,
// for the phone to play a preview. That bucket is empty. A content-hash sweep
// of every blob in all 135 repositories, reachable and unreachable, against the
// SHA1s recorded in the old `file_info` table found none of the 11 sleep tones
// (24,631 blobs, zero matches, 2026-08-26). The only surviving audio anywhere is
// one ringtone, `kasetsu/audio/server/raw/DIG005.raw`.
//
// suripu's own answer is already broken in a way nobody noticed: it signs the
// ringtone URLs against `http://localstack:4566/...`, a Docker-internal
// hostname the phone cannot resolve, with a fresh signature and expiry on every
// call. So previews have not worked here at any point.
//
// So orb omits the URL rather than shipping a dead one. **This costs nothing
// functional.** The id is the entire payload that matters: it is what the app
// writes onto the alarm, and the Sense plays the tone from its own SD card by
// that id, which is the path that actually rang on 2026-08-16. The URL only
// ever drove an in-app preview.
//
// The app tolerates the absence. `SENSound` reads `dict[@"url"]` and
// `SENSleepSounds` uses `SENObjectOfClass(dictionary[@"preview_url"], ...)`,
// both of which yield nil rather than failing to parse.
//
// # When the audio is recovered
//
// It exists in exactly one place: the Sense's own microSD card, at the paths
// `file_info` records (`/SLPTONES/ST010.RAW` and so on), with SHA1s to verify
// against. It cannot be pulled over the wire: `cat` in the 1.9.2 firmware
// prints with `LOGF("%s", ...)` and stops at the first null byte, `fsrd` reads
// serial flash rather than the SD card, and the file-sync protocol only ever
// pushes files to the device. That makes it a teardown, tracked in
// GOING-PUBLIC.md rather than blocking these endpoints.
//
// At that point the change here is small and local: fill in `URL` and
// `PreviewURL` from a served path, the way `insightart` already does for the
// card banners. The lists below do not change.

// AlarmSound is one ringtone in the alarm tone picker.
type AlarmSound struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	// URL is omitted while no audio exists to point it at. `omitempty` rather
	// than a null, because the reference omits nothing and a null would be a
	// third state the app has never been sent.
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

func (h *Handler) getAlarmSounds(w http.ResponseWriter, r *http.Request) {
	if _, ok := AccountFrom(r); !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, alarmSounds)
}

// SleepSound is one sleep tone.
type SleepSound struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	PreviewURL string `json:"preview_url,omitempty"`
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
	{ID: 1, Name: "Aura"},
	{ID: 2, Name: "Nocturne"},
	{ID: 3, Name: "Morpheus"},
	{ID: 4, Name: "Horizon"},
	{ID: 5, Name: "Cosmos"},
	{ID: 6, Name: "Autumn Wind"},
	{ID: 7, Name: "Fireside"},
	{ID: 8, Name: "Rainfall"},
	{ID: 9, Name: "Forest Creek"},
	{ID: 10, Name: "Brown Noise"},
	{ID: 11, Name: "White Noise"},
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
		AvailableSounds:    sleepSoundList{Sounds: sleepSounds, State: sleepSoundsState},
		// The SAME value /v2/sleep_sounds/status serves, not a second copy of
		// the same literal. Two answers to "is a sound playing" that can drift
		// apart is the class of split this consolidation exists to remove, and
		// this endpoint exists precisely so the app can ask both at once.
		Status: sleepSoundsStatus(),
	})
}

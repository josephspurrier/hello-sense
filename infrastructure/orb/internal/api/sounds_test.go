package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// The ringtone list is the reference's, in the reference's order, and both
// halves of that matter.
//
// The order is what the app renders, so reordering renames every tone from the
// user's point of view. The ids are what get written onto the alarm and played
// from the device's SD card, so a wrong id rings the wrong sound or none.
//
// Pinned against the live suripu response captured 2026-08-27. The first
// version of this list had 12 entries because it was transcribed from a
// truncated view of the Java source; apidiff caught the three missing tones.
func TestAlarmSoundsMatchTheReference(t *testing.T) {
	want := []struct {
		id   int
		name string
	}{
		{5, "Dusk"}, {4, "Pulse"}, {6, "Lilt"}, {7, "Bounce"},
		{8, "Celebration"}, {9, "Milky Way"}, {10, "Waves"}, {11, "Lights"},
		{12, "Echo"}, {13, "Drops"}, {14, "Twinkle"}, {15, "Silver"},
		{16, "Highlights"}, {17, "Ripple"}, {18, "Sway"},
	}
	if len(alarmSounds) != len(want) {
		t.Fatalf("got %d ringtones, want %d", len(alarmSounds), len(want))
	}
	for i, w := range want {
		if alarmSounds[i].ID != w.id || alarmSounds[i].Name != w.name {
			t.Errorf("position %d = {%d %q}, want {%d %q}",
				i, alarmSounds[i].ID, alarmSounds[i].Name, w.id, w.name)
		}
	}
}

// Ids are unique, in both lists. A duplicate would make one tone unselectable
// and is the kind of thing a hand-maintained list acquires.
func TestSoundIDsAreUnique(t *testing.T) {
	seen := map[int]string{}
	for _, s := range alarmSounds {
		if prev, dup := seen[s.ID]; dup {
			t.Errorf("ringtone id %d used by both %q and %q", s.ID, prev, s.Name)
		}
		seen[s.ID] = s.Name
	}
	seen = map[int]string{}
	for _, s := range sleepSounds {
		if prev, dup := seen[s.ID]; dup {
			t.Errorf("sleep sound id %d used by both %q and %q", s.ID, prev, s.Name)
		}
		seen[s.ID] = s.Name
	}
}

// The sleep tones must match the `file_info` rows, because each one names a
// real file on the device's SD card. If these drift, the app offers a sound the
// Sense cannot play.
func TestSleepSoundsMatchFileInfo(t *testing.T) {
	want := []string{
		"Aura", "Nocturne", "Morpheus", "Horizon", "Cosmos", "Autumn Wind",
		"Fireside", "Rainfall", "Forest Creek", "Brown Noise", "White Noise",
	}
	if len(sleepSounds) != len(want) {
		t.Fatalf("got %d sleep sounds, want %d", len(sleepSounds), len(want))
	}
	for i, w := range want {
		if sleepSounds[i].Name != w {
			t.Errorf("position %d = %q, want %q", i, sleepSounds[i].Name, w)
		}
		if sleepSounds[i].ID != i+1 {
			t.Errorf("%q has id %d, want %d", w, sleepSounds[i].ID, i+1)
		}
	}
}

// The audio URL fields are OMITTED, not null and not empty.
//
// This is the deliberate divergence from suripu, and the test exists so that
// serving a dead URL again is a decision rather than an accident. The app reads
// these with nil-tolerant lookups (`dict[@"url"]`,
// `SENObjectOfClass(dictionary[@"preview_url"], ...)`), so an absent key parses
// as no preview rather than as a parse failure.
//
// When the audio is recovered from the SD card, this test is what should change
// first, and changing it should be a conscious edit.
func TestNoAudioURLsAreServed(t *testing.T) {
	b, err := json.Marshal(alarmSounds)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"url"`) {
		t.Errorf("ringtones carry a url field: %s", b)
	}
	if strings.Contains(string(b), "hello-audio") || strings.Contains(string(b), "localstack") {
		t.Errorf("ringtones point at dead audio: %s", b)
	}

	b, err = json.Marshal(sleepSounds)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "preview_url") {
		t.Errorf("sleep sounds carry a preview_url field: %s", b)
	}
	if strings.Contains(string(b), "s3.amazonaws.com") {
		t.Errorf("sleep sounds point at the dead bucket: %s", b)
	}
}

// The nesting is the app's contract: `availableSounds` wraps a `sounds` array
// and carries its own `state`, and `availableDurations` wraps `durations`.
// Flattening either would parse as empty and the picker would be blank.
func TestCombinedStateShape(t *testing.T) {
	b, err := json.Marshal(CombinedSleepSoundState{
		AvailableDurations: sleepDurationList{Durations: sleepDurations},
		AvailableSounds:    sleepSoundList{Sounds: sleepSounds, State: sleepSoundsState},
		Status:             sleepSoundsStatus(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		`"availableDurations"`, `"durations"`,
		`"availableSounds"`, `"sounds"`, `"state":"OK"`,
		`"status"`, `"playing":false`, `"volume_percent":null`,
	} {
		if !strings.Contains(string(b), key) {
			t.Errorf("missing %s in %s", key, b)
		}
	}
}

// The status block and GET /v2/sleep_sounds/status must be the same payload.
// Two answers to "is a sound playing" that can drift apart is the class of
// split this consolidation exists to remove.
func TestCombinedStatusMatchesTheStatusEndpoint(t *testing.T) {
	a, _ := json.Marshal(sleepSoundsStatus())
	combined, _ := json.Marshal(CombinedSleepSoundState{Status: sleepSoundsStatus()}.Status)
	if string(a) != string(combined) {
		t.Errorf("status endpoint = %s, combined_state status = %s", a, combined)
	}
}

// Durations are the reference's fixed six. "Indefinitely" is a real option, not
// a placeholder: it plays until stopped.
func TestSleepDurations(t *testing.T) {
	want := []string{"10 Minutes", "30 Minutes", "1 Hour", "2 Hours", "3 Hours", "Indefinitely"}
	if len(sleepDurations) != len(want) {
		t.Fatalf("got %d durations, want %d", len(sleepDurations), len(want))
	}
	for i, w := range want {
		if sleepDurations[i].Name != w || sleepDurations[i].ID != i+1 {
			t.Errorf("position %d = {%d %q}, want {%d %q}",
				i, sleepDurations[i].ID, sleepDurations[i].Name, i+1, w)
		}
	}
}

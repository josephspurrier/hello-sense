package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/josephspurrier/hello-orb/orb/internal/api/soundpreview"
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

// Every sound the app can pick has an embedded preview, and the mapping from
// display name to file is pinned.
//
// This test used to assert the opposite: that no URL fields were served,
// because the audio was lost with the `hello-audio` bucket. It was recovered
// from a Sense's SD card on 2026-08-31 (the sleep tones verify byte-exact
// against the `file_info_one_five` SHA1s), so now the previews are load-bearing.
//
// The ringtone mapping goes through alarm.SoundPath on purpose: the preview
// must be the file the device will ring. The literal table here pins that
// mapping too, so an edit to SoundPath that silently reshuffles the tones
// fails here as well as on the device.
func TestEverySoundHasAnEmbeddedPreview(t *testing.T) {
	ringtones := map[int]string{
		4: "DIG001.mp3", 5: "DIG002.mp3", 6: "DIG003.mp3", 7: "DIG004.mp3",
		8: "DIG005.mp3", 9: "NEW001.mp3", 10: "NEW002.mp3", 11: "NEW003.mp3",
		12: "NEW004.mp3", 13: "NEW005.mp3", 14: "NEW006.mp3", 15: "ORG001.mp3",
		16: "ORG002.mp3", 17: "ORG003.mp3", 18: "ORG004.mp3",
	}
	for _, s := range alarmSounds {
		got := ringtonePreviewFile(s.ID)
		if want := ringtones[s.ID]; got != want {
			t.Errorf("%q (id %d) previews %s, want %s", s.Name, s.ID, got, want)
		}
		if !soundpreview.Has(got) {
			t.Errorf("%q (id %d) has no embedded preview %s", s.Name, s.ID, got)
		}
	}
	for _, s := range sleepSounds {
		if !soundpreview.Has(s.file) {
			t.Errorf("%q has no embedded preview %q", s.Name, s.file)
		}
	}
}

// The served URLs are absolute, built from the origin the phone reached us on,
// and point nowhere near the dead bucket. The app hands the string to
// `[NSURL URLWithString:]` with no base, so a relative path would silently
// play nothing.
func TestPreviewURLsAreBuiltFromTheRequest(t *testing.T) {
	r := httptest.NewRequest("GET", "https://sense.example.com:8443/v2/sleep_sounds/combined_state", nil)

	for _, s := range sleepSoundsWithPreviews(r) {
		want := "https://sense.example.com:8443" + soundPreviewPath + s.file
		if s.PreviewURL != want {
			t.Errorf("%q preview_url = %q, want %q", s.Name, s.PreviewURL, want)
		}
	}

	b, err := json.Marshal(sleepSoundsWithPreviews(r))
	if err != nil {
		t.Fatal(err)
	}
	for _, dead := range []string{"hello-audio", "localstack", "s3.amazonaws.com"} {
		if strings.Contains(string(b), dead) {
			t.Errorf("sleep sounds point at dead audio: %s", b)
		}
	}
	// The static list stays URL-free: the fill happens per request, so a
	// stale origin can never be baked in.
	b, err = json.Marshal(sleepSounds)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "preview_url") {
		t.Errorf("static sleep sound list carries a preview_url: %s", b)
	}
	b, err = json.Marshal(alarmSounds)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"url"`) {
		t.Errorf("static ringtone list carries a url: %s", b)
	}
}

// The handler serves real audio with immutable caching and refuses anything
// that is not one of ours, exactly like the insight art handler it mirrors.
func TestPreviewHandlerServesAudioAndRefusesAnythingElse(t *testing.T) {
	h := soundpreview.Handler(soundPreviewPath)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", soundPreviewPath+"DIG002.mp3", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "audio/mpeg") {
		t.Errorf("content-type = %q", ct)
	}
	if rec.Body.Len() < 10*1024 {
		t.Errorf("body = %d bytes, too small to be a ringtone", rec.Body.Len())
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("cache-control = %q; the app refetches every preview without it", cc)
	}

	for _, bad := range []string{
		soundPreviewPath,
		soundPreviewPath + "nope.mp3",
		soundPreviewPath + "../sounds.go",
		soundPreviewPath + "ST005.mp3", // Ocean Waves: real on the card, never offered
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", bad, nil))
		if rec.Code != 404 {
			t.Errorf("%s: status = %d, want 404", bad, rec.Code)
		}
	}

	// A stray embedded file means the audio directory and the lists have
	// diverged: 15 ringtones plus 11 sleep tones.
	if got, want := len(soundpreview.Names()), len(alarmSounds)+len(sleepSounds); got != want {
		t.Errorf("embedded file count = %d, want %d", got, want)
	}
}

// The nesting is the app's contract: `availableSounds` wraps a `sounds` array
// and carries its own `state`, and `availableDurations` wraps `durations`.
// Flattening either would parse as empty and the picker would be blank.
func TestCombinedStateShape(t *testing.T) {
	b, err := json.Marshal(CombinedSleepSoundState{
		AvailableDurations: sleepDurationList{Durations: sleepDurations},
		AvailableSounds:    sleepSoundList{Sounds: sleepSounds, State: sleepSoundsState},
		Status:             SleepSoundsStatus{Playing: false},
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

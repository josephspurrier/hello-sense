package api

import (
	"github.com/josephspurrier/hello-orb/orb/internal/store"
	"testing"
	"time"
)

// The sound calibration is the one place orb deliberately reproduces a defect in
// the reference, so it gets the only pinned values in this package.
//
// Both pairs are real: the stored millidecibels came out of sensor_samples, and
// the expected decibels came off the running Java stack through cmd/apidiff on
// the same minute. If the 1000/1024 re-read in reReadAudio is ever "fixed",
// these fail with a number about 1 dB high, which is exactly how the bug
// presented in the first place.
func TestCalibratedSoundMatchesReference(t *testing.T) {
	cases := []struct {
		name   string
		stored int32
		want   float32
	}{
		{"observed 2026-08-15a", 43614, 27.591},
		{"observed 2026-08-15b", 44745, 28.696},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := calibratedSound(c.stored, 0); got != c.want {
				t.Errorf("calibratedSound(%d) = %v, want %v", c.stored, got, c.want)
			}
		})
	}
}

// A zero energy reading is absence, not silence: the reference falls through to
// the disturbance column rather than reporting a 25 dB room.
func TestCalibratedSoundFallsBackToDisturbances(t *testing.T) {
	got := calibratedSound(0, 43614)
	if want := float32(27.591); got != want {
		t.Errorf("fallback = %v, want %v", got, want)
	}
}

// Below the noise floor the result floors at 0 rather than going negative, and
// the floor is applied after the artificial floor is added back, not before.
func TestCalibratedSoundFloorsAtZero(t *testing.T) {
	if got := calibratedSound(1000, 0); got != 0 {
		t.Errorf("calibratedSound(1000) = %v, want 0", got)
	}
}

// The dials must stop claiming to describe the room once the data is old.
//
// This gap was found the hard way: the reference's own ingest stalled and it
// answered UNKNOWN while orb went on serving readings from hours earlier as
// though they were live. Showing a stale number is worse than showing none,
// because the entire point of the screen is that it is current.
func TestSensorStaleness(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		age  time.Duration
		want bool
	}{
		{"just taken", 0, false},
		{"a few minutes", 5 * time.Minute, false},
		{"exactly at the threshold", sensorFreshness, false},
		{"a second past it", sensorFreshness + time.Second, true},
		{"hours old", 3 * time.Hour, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := isStale(now.Add(-c.age), now); got != c.want {
				t.Errorf("age %v: isStale = %v, want %v", c.age, got, c.want)
			}
		})
	}
}

// The dust conversion, including the floor that stops a clean room reading zero.
//
// The offset is a calibration delta on the RAW COUNT, applied before the
// conversion. Applying it to the density instead gives a plausible number that
// is wrong by the conversion factor, which is the kind of error nobody notices.
func TestCalibratedParticulates(t *testing.T) {
	// Uncalibrated: no delta at all. (2048/4095) * 4.1076 * (0.5/2.9) * 1000.
	if got := calibratedParticulates(2048, nil); got < 354.0 || got > 355.0 {
		t.Errorf("mid-scale uncalibrated = %v, want ~354.4", got)
	}
	// Floored: a zero count must not read as zero density.
	if got := calibratedParticulates(0, nil); got != 1.0 {
		t.Errorf("zero count = %v, want 1.0 (the 0.001 floor, scaled)", got)
	}
	// A count driven negative by the delta still floors.
	big := int32(1000)
	if got := calibratedParticulates(10, &big); got != 1.0 {
		t.Errorf("negative after delta = %v, want 1.0", got)
	}
}

// An uncalibrated device must not be treated as one calibrated to zero. Offset
// zero derives a delta of +300, which would silently inflate every reading.
func TestUncalibratedIsNotOffsetZero(t *testing.T) {
	zero := int32(0)
	uncal := calibratedParticulates(510, nil)
	asZero := calibratedParticulates(510, &zero)
	if uncal == asZero {
		t.Fatal("nil offset behaved identically to offset 0")
	}
	if asZero <= uncal {
		t.Errorf("offset 0 (%v) should read HIGHER than uncalibrated (%v): delta is +300", asZero, uncal)
	}
}

// The delta is DERIVED from the stored offset, and the rounding is Java's.
//
// Java's Math.round rounds half toward positive infinity, so 300 - 395*1.3 =
// -213.5 becomes -213. Go's math.Round rounds half away from zero and would
// give -214. This device's real offset is 395, so the disagreement is on
// exactly the value in use.
func TestDustDelta(t *testing.T) {
	for _, c := range []struct{ offset, want int32 }{
		{395, -213}, // this device: -213.5, and Java rounds it UP
		{0, 300},    // uncalibrated offset gives the base
		{100, 170},  // 300 - 130
		{300, -90},  // 300 - 390
	} {
		if got := dustDelta(c.offset); got != c.want {
			t.Errorf("dustDelta(%d) = %d, want %d", c.offset, got, c.want)
		}
	}
}

// With this device's real calibration, orb must land where the reference lands.
// Uncalibrated it reads about 90 for the same counts, which is the bug that hid
// air quality in the first place.
func TestParticulatesMatchesCalibratedReference(t *testing.T) {
	offset := int32(395)
	got := calibratedParticulates(510, &offset)
	if got < 50.0 || got > 53.0 {
		t.Errorf("calibrated = %v, want ~51 (the reference reads ~53 on a nearby sample)", got)
	}
	if un := calibratedParticulates(510, nil); un < 80.0 {
		t.Errorf("uncalibrated = %v, expected ~90 to show the calibration matters", un)
	}
}

// The first two bands are both green, which is easy to get wrong: "Moderate"
// carries IDEAL, not WARNING.
func TestParticulatesBands(t *testing.T) {
	for _, c := range []struct {
		value           float32
		name, condition string
	}{
		{0, "Ideal", "IDEAL"},
		{49.9, "Ideal", "IDEAL"},
		{50, "Moderate", "IDEAL"},
		{99.9, "Moderate", "IDEAL"},
		{100, "Unhealthy for sensitive groups", "WARNING"},
		{150, "Unhealthy", "WARNING"},
		{200, "Very unhealthy", "ALERT"},
		{300, "Hazardous", "ALERT"},
		// Past the top band, and in the 0.1 gap between two bands: matches
		// nothing, and the reference's fromScale answers UNKNOWN for both
		// (it logs not-in-range). An earlier version fell back to the last
		// band, which turned a reading of 49.95 into "Hazardous".
		{5000, "", "UNKNOWN"},
		{49.95, "", "UNKNOWN"},
	} {
		iv := classify(c.value, particulatesScale)
		if iv.Name != c.name || iv.Condition != c.condition {
			t.Errorf("%v gave %q/%s, want %q/%s", c.value, iv.Name, iv.Condition, c.name, c.condition)
		}
	}
}

// The endpoint's shape: which dials, in what order.
//
// This exists because a reordering change dropped the SOUND dial entirely and
// every other test in this file still passed. They all exercise the calibration
// functions; none of them asserted what /v2/sensors actually returns. The
// omission was caught by apidiff against the running reference, which is a slow
// and lucky way to find a missing field.
//
// The order matches the reference and is part of the app's contract, not a
// presentation detail: the app reads this array positionally in places.
func TestSensorViewsShape(t *testing.T) {
	offset := int32(395)
	sample := &store.LatestSampleRow{
		TS:                      time.Now(),
		Temperature:             2535,
		Humidity:                4049,
		Light:                   2156,
		AudioPeakEnergyDB:       43614,
		AudioPeakDisturbancesDB: 10,
		AirQualityRaw:           527,
		DustOffset:              &offset,
	}

	want := []string{"TEMPERATURE", "HUMIDITY", "LIGHT", "PARTICULATES", "SOUND"}

	views := sensorViews(sample, "", false, nil)
	got := make([]string, 0, len(views))
	for _, v := range views {
		got = append(got, v.Type)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d sensors %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sensor %d is %s, want %s (full order %v)", i, got[i], want[i], got)
		}
	}

	// Every dial carries a scale and a condition, or the app draws an empty
	// gauge. A sensor present but blank is the failure this half catches.
	for _, v := range views {
		if len(v.Scale) == 0 {
			t.Errorf("%s has no scale", v.Type)
		}
		if v.Condition == "" {
			t.Errorf("%s has no condition", v.Type)
		}
		if v.Value == nil {
			t.Errorf("%s has no value on a fresh sample", v.Type)
		}
	}
}

// A stale sample keeps every dial but empties it. The count must not change:
// the app draws the gauges either way.
func TestSensorViewsStaleKeepsAllDials(t *testing.T) {
	sample := &store.LatestSampleRow{TS: time.Now().Add(-2 * time.Hour)}
	views := sensorViews(sample, "", true, nil)
	if len(views) != 5 {
		t.Fatalf("stale gave %d sensors, want 5", len(views))
	}
	for _, v := range views {
		if v.Value != nil || v.Condition != "UNKNOWN" {
			t.Errorf("%s: stale should be null/UNKNOWN, got %v/%s", v.Type, v.Value, v.Condition)
		}
		if len(v.Scale) == 0 {
			t.Errorf("%s: stale must keep its scale", v.Type)
		}
	}
}

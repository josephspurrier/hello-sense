package alarm

import (
	"testing"
	"time"
)

var eastern = mustLoad("America/New_York")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// at builds a local wall-clock instant in Eastern.
func at(y int, m time.Month, d, hh, mm int) time.Time {
	return time.Date(y, m, d, hh, mm, 0, 0, eastern)
}

// 2026-08-17 is a Monday, which every case below leans on.
func TestNextRepeatingAlarm(t *testing.T) {
	weekdays := Alarm{Enabled: true, Repeated: true, Hour: 7, Minute: 0,
		DayOfWeek: []int{1, 2, 3, 4, 5}, SoundID: 5}

	for _, c := range []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"monday before the alarm", at(2026, 8, 17, 6, 30), at(2026, 8, 17, 7, 0)},
		{"monday after it", at(2026, 8, 17, 7, 30), at(2026, 8, 18, 7, 0)},
		{"friday after it rolls to monday", at(2026, 8, 21, 9, 0), at(2026, 8, 24, 7, 0)},
		{"saturday rolls to monday", at(2026, 8, 22, 12, 0), at(2026, 8, 24, 7, 0)},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := Next([]Alarm{weekdays}, c.now, eastern)
			if got == nil {
				t.Fatal("no ring")
			}
			if !got.At.Equal(c.want.UTC()) {
				t.Errorf("ring at %v, want %v", got.At.In(eastern), c.want)
			}
		})
	}
}

// A ring that has already happened is never reported as due again.
//
// This is the one place orb deliberately departs from the reference, and it
// cost a real observation to find. The reference floors now to the minute
// before comparing, which it can afford because it keeps ring history and the
// device acknowledges each ring. orb has neither, so the floor made an alarm at
// 21:05:00 still look due at 21:05:44, and the edge duly reported "ring in 1
// second" for the rest of that minute. On a device that is listening, that is a
// second ring a minute after the first.
func TestPassedAlarmIsNotServedAgain(t *testing.T) {
	a := Alarm{Enabled: true, Repeated: true, Hour: 7, Minute: 0,
		DayOfWeek: []int{1}, SoundID: 1}

	// Well inside the same minute as the ring, which is what used to re-trigger.
	got := Next([]Alarm{a}, time.Date(2026, 8, 17, 7, 0, 44, 0, eastern), eastern)
	if got == nil {
		t.Fatal("no ring at all")
	}
	if want := at(2026, 8, 24, 7, 0); !got.At.Equal(want.UTC()) {
		t.Errorf("ring at %v, want next week %v: an alarm that has passed must not be re-served",
			got.At.In(eastern), want)
	}

	// A second before it is still due, so the boundary is not over-corrected.
	got = Next([]Alarm{a}, time.Date(2026, 8, 17, 6, 59, 59, 0, eastern), eastern)
	if got == nil {
		t.Fatal("no ring")
	}
	if want := at(2026, 8, 17, 7, 0); !got.At.Equal(want.UTC()) {
		t.Errorf("ring at %v, want %v", got.At.In(eastern), want)
	}
}

// A one-off alarm fires on its date and then never again.
func TestNonRepeatingAlarm(t *testing.T) {
	a := Alarm{Enabled: true, Hour: 6, Minute: 15,
		Year: 2026, Month: 8, Day: 18, SoundID: 2}

	got := Next([]Alarm{a}, at(2026, 8, 17, 22, 0), eastern)
	if got == nil {
		t.Fatal("no ring before the date")
	}
	if want := at(2026, 8, 18, 6, 15); !got.At.Equal(want.UTC()) {
		t.Errorf("ring at %v, want %v", got.At.In(eastern), want)
	}

	if got := Next([]Alarm{a}, at(2026, 8, 18, 7, 0), eastern); got != nil {
		t.Errorf("expired one-off still rang at %v", got.At)
	}
}

// A disabled alarm never rings, and neither does a one-off with no date. The
// second is the safer failure: ringing on a guessed date is worse than silence.
func TestAlarmsThatMustNotRing(t *testing.T) {
	now := at(2026, 8, 17, 6, 0)
	for _, c := range []struct {
		name string
		a    Alarm
	}{
		{"disabled", Alarm{Enabled: false, Repeated: true, Hour: 7, DayOfWeek: []int{1}}},
		{"one-off with no date", Alarm{Enabled: true, Hour: 7, Minute: 0}},
		{"repeating with no days", Alarm{Enabled: true, Repeated: true, Hour: 7}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Next([]Alarm{c.a}, now, eastern); got != nil {
				t.Errorf("rang at %v, want no ring", got.At)
			}
		})
	}
	if got := Next(nil, now, eastern); got != nil {
		t.Errorf("no alarms produced a ring at %v", got.At)
	}
}

// The earliest wins, across a mixed set.
func TestEarliestAlarmWins(t *testing.T) {
	early := Alarm{Enabled: true, Repeated: true, Hour: 6, Minute: 30, DayOfWeek: []int{1}, SoundID: 9}
	late := Alarm{Enabled: true, Repeated: true, Hour: 8, Minute: 0, DayOfWeek: []int{1}, SoundID: 3}
	oneOff := Alarm{Enabled: true, Hour: 7, Minute: 0, Year: 2026, Month: 8, Day: 17, SoundID: 4}

	got := Next([]Alarm{late, oneOff, early}, at(2026, 8, 17, 5, 0), eastern)
	if got == nil {
		t.Fatal("no ring")
	}
	if want := at(2026, 8, 17, 6, 30); !got.At.Equal(want.UTC()) {
		t.Errorf("ring at %v, want %v", got.At.In(eastern), want)
	}
	if got.SoundID != 9 {
		t.Errorf("sound %d, want 9 from the winning alarm", got.SoundID)
	}
}

// An alarm is a wall-clock promise, so it must survive the clocks changing.
// 2026-11-01 is the US autumn transition: seven o'clock is seven o'clock on
// both sides of it, even though the days are not the same length.
func TestAlarmSurvivesDaylightSaving(t *testing.T) {
	a := Alarm{Enabled: true, Repeated: true, Hour: 7, Minute: 0,
		DayOfWeek: []int{1, 2, 3, 4, 5, 6, 7}, SoundID: 1}

	got := Next([]Alarm{a}, at(2026, 10, 31, 12, 0), eastern)
	if got == nil {
		t.Fatal("no ring")
	}
	local := got.At.In(eastern)
	if local.Hour() != 7 || local.Minute() != 0 {
		t.Errorf("rang at %v, want 07:00 local across the DST change", local)
	}
	if local.Day() != 1 || local.Month() != time.November {
		t.Errorf("rang on %v, want 1 November", local)
	}
}

// The odd spellings are the reference's and the device reads these paths off
// its own flash, so a tidier scheme would simply not play.
func TestSoundPath(t *testing.T) {
	for id, want := range map[int]string{
		0:  "/RINGTONE/DIGO000.raw",
		3:  "/RINGTONE/DIGO003.raw",
		4:  "/RINGTONE/DIG001.raw",
		5:  "/RINGTONE/DIG002.raw",
		8:  "/RINGTONE/DIG005.raw",
		9:  "/RINGTONE/NEW001.raw",
		14: "/RINGTONE/NEW006.raw",
		15: "/RINGTONE/ORG001.raw",
		18: "/RINGTONE/ORG004.raw",
		99: "/RINGTONE/DIG001.raw", // unknown falls back rather than silent
		-1: "/RINGTONE/DIG001.raw",
	} {
		if got := SoundPath(id); got != want {
			t.Errorf("SoundPath(%d) = %q, want %q", id, got, want)
		}
	}
}

// The three awake signals, each on its own, using the reference's thresholds.
func TestStirring(t *testing.T) {
	for _, c := range []struct {
		name string
		m    []Motion
		want bool
	}{
		{"nothing at all", nil, false},
		{"lying still", []Motion{{100, 0, 0}, {200, 0, 1}}, false},

		{"one strong kick", []Motion{{100, 6, 0}}, true},
		{"kicks at the threshold are not enough", []Motion{{100, 5, 0}}, false},

		{"a long burst of movement", []Motion{{100, 0, 10}}, true},
		{"on-duration at the threshold is not enough", []Motion{{100, 0, 9}}, false},

		// Amplitude needs TWO minutes, which is what stops a single roll over
		// from being read as waking up.
		{"one big movement", []Motion{{5000, 0, 0}}, false},
		{"two big movements", []Motion{{5000, 0, 0}, {4501, 0, 0}}, true},
		{"amplitude at the threshold is not enough", []Motion{{4500, 0, 0}, {4500, 0, 0}}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Stirring(c.m); got != c.want {
				t.Errorf("Stirring = %v, want %v", got, c.want)
			}
		})
	}
}

// The alarm is a promise: never earlier than the window, never later than asked,
// and unchanged without evidence.
func TestBringForward(t *testing.T) {
	set := at(2026, 8, 17, 7, 0)
	stirring := []Motion{{100, 6, 0}}
	still := []Motion{{100, 0, 0}}

	for _, c := range []struct {
		name string
		now  time.Time
		m    []Motion
		want time.Time
	}{
		{"long before the window, even if stirring", at(2026, 8, 17, 5, 0), stirring, set},
		{"just outside the window", at(2026, 8, 17, 6, 29), stirring, set},

		{"inside the window and stirring", at(2026, 8, 17, 6, 40), stirring, at(2026, 8, 17, 6, 40)},
		{"at the window edge and stirring", at(2026, 8, 17, 6, 30), stirring, at(2026, 8, 17, 6, 30)},

		{"inside the window but still asleep", at(2026, 8, 17, 6, 40), still, set},
		{"inside the window with no data at all", at(2026, 8, 17, 6, 40), nil, set},

		{"at the set time", set, stirring, set},
		{"past the set time", at(2026, 8, 17, 7, 5), stirring, set},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := BringForward(set, c.now, c.m)
			if !got.Equal(c.want) {
				t.Errorf("ring at %v, want %v", got, c.want)
			}
			if got.After(set) {
				t.Errorf("rang LATER than the alarm was set for: %v > %v", got, set)
			}
			if got.Before(set.Add(-SmartWindow)) {
				t.Errorf("rang before the window opened: %v", got)
			}
		})
	}
}

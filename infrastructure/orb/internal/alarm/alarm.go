// Package alarm decides when the Sense should next ring.
//
// This is the one part of orb whose output is a physical event in somebody's
// bedroom. Everything else here shows a wrong number on a screen when it is
// wrong; this wakes a person at the wrong time, or fails to wake them at all.
// It is written to be dull and testable for that reason.
//
// Ordinary alarms only. Smart wake, which moves the ring earlier inside a
// window when the sleeper looks to be in light sleep, is NOT implemented: a
// smart alarm here rings at its set time, which is the safe direction to be
// wrong in. The reference's RingProcessor is 832 lines and most of it is that
// feature.
package alarm

import "time"

// Alarm is one alarm template, as the app set it.
type Alarm struct {
	Enabled  bool
	Repeated bool
	Hour     int
	Minute   int
	// DayOfWeek is ISO: Monday is 1, Sunday is 7. Only for repeated alarms.
	DayOfWeek []int
	// Year, Month, Day fix a non-repeating alarm to one date.
	Year, Month, Day int
	SoundID          int
	// Smart alarms may ring early; see BringForward.
	Smart bool
}

// Ring is the next moment an alarm should sound.
type Ring struct {
	At      time.Time // UTC
	SoundID int
	// Smart marks a ring that may be brought forward. Carried here rather than
	// looked up again, because the alarm that won is the one whose flag counts.
	Smart bool
}

// Next returns the earliest ring at or after now, or nil when nothing is set.
//
// loc is the zone the alarm was set in, and it matters more than it looks: an
// alarm is a wall-clock promise. Seven o'clock means seven o'clock where the
// sleeper is, so this works in local time throughout and converts back at the
// end.
func Next(alarms []Alarm, now time.Time, loc *time.Location) *Ring {
	// Compared against the EXACT local time, deliberately unlike the reference,
	// which floors to the minute first.
	//
	// The reference can afford the floor because it keeps ring history and the
	// device acknowledges each ring, so an alarm already served is not served
	// again. orb has neither, and with the floor an alarm at 21:05:00 is still
	// "not before" 21:05:44, so every sync for the rest of that minute reports
	// it as due. Observed live on 2026-08-15: the countdown ran 257, 198, 138,
	// 78, 18 correctly and then said 1 again at 21:05:44, which on a device
	// that is listening is a second ring a minute after the first.
	//
	// The cost is that an alarm created in the same minute it is due may wait
	// for the following occurrence. That is the safe direction to be wrong in:
	// a missed degenerate case against an alarm that will not stop.
	local := now.In(loc)

	var best *Ring
	consider := func(at time.Time, soundID int, smart bool) {
		if at.Before(local) {
			return
		}
		if best == nil || at.Before(best.At) {
			best = &Ring{At: at.UTC(), SoundID: soundID, Smart: smart}
		}
	}

	for _, a := range alarms {
		if !a.Enabled {
			continue
		}
		if a.Repeated {
			for _, dow := range a.DayOfWeek {
				// Days from today to that weekday, which may be negative; the
				// week rollover below fixes those.
				diff := dow - isoWeekday(local)
				day := startOfDay(local).AddDate(0, 0, diff)
				at := time.Date(day.Year(), day.Month(), day.Day(), a.Hour, a.Minute, 0, 0, loc)
				if at.Before(local) {
					// Already gone this week, so it is next week's.
					at = at.AddDate(0, 0, 7)
				}
				consider(at, a.SoundID, a.Smart)
			}
			continue
		}
		// A non-repeating alarm is fixed to its date and simply expires. An
		// incomplete date means the row predates this field or was written
		// wrong; ringing on a guessed date is worse than not ringing.
		if a.Year == 0 || a.Month == 0 || a.Day == 0 {
			continue
		}
		consider(time.Date(a.Year, time.Month(a.Month), a.Day, a.Hour, a.Minute, 0, 0, loc), a.SoundID, a.Smart)
	}
	return best
}

// isoWeekday is Monday=1 through Sunday=7, which is how the app stores
// day_of_week. Go's Weekday is Sunday=0, and the two disagree on every day.
func isoWeekday(t time.Time) int {
	if d := int(t.Weekday()); d != 0 {
		return d
	}
	return 7
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// SoundPath maps a ringtone id to the file on the Sense's flash.
//
// Copied from the reference, odd spellings included: ids 0 to 3 are "DIGO00"
// with a letter O, and the rest are not. An unknown id falls back to DIG001
// rather than to silence, because an alarm that plays the wrong tone still
// wakes somebody and one that plays nothing does not.
func SoundPath(soundID int) string {
	switch {
	case soundID >= 0 && soundID <= 3:
		return "/RINGTONE/DIGO00" + itoa(soundID) + ".raw"
	case soundID >= 4 && soundID <= 8:
		return "/RINGTONE/DIG00" + itoa(soundID-3) + ".raw"
	case soundID >= 9 && soundID <= 14:
		return "/RINGTONE/NEW00" + itoa(soundID-8) + ".raw"
	case soundID >= 15 && soundID <= 18:
		return "/RINGTONE/ORG00" + itoa(soundID-14) + ".raw"
	}
	return "/RINGTONE/DIG001.raw"
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// Smart wake: bringing an alarm forward when the sleeper is already surfacing.
//
// This is a DELIBERATE DIVERGENCE from the reference, and the reason is worth
// stating plainly. `SleepCycleAlgorithm.getSmartAlarmTimeUTC` has three paths:
// when it detects light sleep it rings at `random.nextInt(span)`; when it
// detects deep sleep it guesses the next light moment by adding a hard-coded
// 1.5 hour cycle; and when neither fits it calls `fakeSmartAlarm`, which its own
// author named that, and which returns a uniformly random time using no sleep
// data at all. Two of the three paths are randomised and the third is fixed
// arithmetic.
//
// Reproducing that would mean putting java.util.Random in the code path that
// decides when a person wakes up, and it could not be tested: a random ring
// time cannot be diffed against anything.
//
// So this rings early only on evidence, and otherwise rings exactly when asked.
// The evidence is the reference's OWN awake test, `isUserAwakeInGivenDataSpan`,
// which is deterministic and is the one part of that file not built on a coin
// flip. The thresholds below are its constants, unchanged.

// SmartWindow is how far ahead of the set time a smart alarm may ring.
//
// Thirty minutes, matching the reference's smart alarm processing window. An
// alarm never rings before this, and never after its set time, so the worst case
// is exactly the alarm the sleeper asked for.
const SmartWindow = 30 * time.Minute

// The reference's awake thresholds, from SleepCycleAlgorithm.
const (
	awakeAmplitudeMilliG   = 4500
	awakeAmplitudeCount    = 2
	awakeKickoffThreshold  = 5
	awakeOnDurationSeconds = 9
)

// Motion is one minute of pill data.
type Motion struct {
	AmplitudeMilliG int64 // svm_no_gravity
	KickoffCounts   int32
	OnDurationSecs  int32
}

// Stirring reports whether a span of motion looks like someone surfacing.
//
// Straight from `isUserAwakeInGivenDataSpan`: any single strong kick, or any
// long burst of movement, or two minutes above the amplitude threshold. The
// three are ORed, so one clear signal is enough.
func Stirring(m []Motion) bool {
	for _, s := range m {
		if int(s.KickoffCounts) > awakeKickoffThreshold {
			return true
		}
	}
	for _, s := range m {
		if int(s.OnDurationSecs) > awakeOnDurationSeconds {
			return true
		}
	}
	count := 0
	for _, s := range m {
		if s.AmplitudeMilliG > awakeAmplitudeMilliG {
			count++
			if count >= awakeAmplitudeCount {
				return true
			}
		}
	}
	return false
}

// BringForward returns when a smart alarm should actually ring.
//
// Never earlier than SmartWindow before the set time, never later than the set
// time, and unchanged unless there is evidence. No data means no evidence,
// which means the alarm the sleeper set: an alarm is a promise, and guessing
// against it is how smart wake earns its reputation.
func BringForward(setTime, now time.Time, recent []Motion) time.Time {
	if now.Before(setTime.Add(-SmartWindow)) {
		return setTime // too early to consider
	}
	if !now.Before(setTime) {
		return setTime // the moment has arrived or passed
	}
	if Stirring(recent) {
		return now
	}
	return setTime
}

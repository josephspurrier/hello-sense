// Package feedback decides whether a correction a person made to their
// timeline is one the system will accept.
//
// This is the gate in front of the three write endpoints: PATCH moves an event
// to a new time, PUT marks the algorithm's answer correct, DELETE marks it
// wrong. All three build the same record and run it past the same two rules,
// and a record that fails either is refused with 412 and never stored.
//
// The rules matter more than they look. Feedback is not a comment: it is
// training data, and it feeds back into the model that scores every later
// night. A correction saying somebody fell asleep after they woke up teaches a
// path that cannot happen, so the checks here are what stop one mis-tap from
// degrading the model.
package feedback

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Event types, numbered as the app and the database both number them. These
// values are on the wire and in timeline_feedback.event_type, so they are not
// ours to renumber.
const (
	InBed    int32 = 11
	Sleep    int32 = 12
	OutOfBed int32 = 13
	WakeUp   int32 = 14
)

// eventNames maps the names the app puts in the URL to those numbers.
//
// Note GOT_OUT_OF_BED is 13 and WOKE_UP is 14, which is not the order they
// happen in. The numbering is historical and the intended ordering is a
// separate table below; deriving one from the other puts waking after getting
// out of bed.
var eventNames = map[string]int32{
	"GOT_IN_BED":     InBed,
	"FELL_ASLEEP":    Sleep,
	"GOT_OUT_OF_BED": OutOfBed,
	"WOKE_UP":        WakeUp,
}

// EventTypeFromName resolves the URL segment. Unknown names are rejected rather
// than defaulted: a typo must not silently record a correction to the wrong
// event.
func EventTypeFromName(name string) (int32, bool) {
	t, ok := eventNames[strings.ToUpper(name)]
	return t, ok
}

// intendedOrder is the sequence these four events must occur in. Only these
// four are ordered; anything else is not a sleep event and is not checked.
var intendedOrder = map[int32]int{
	InBed:    1,
	Sleep:    2,
	WakeUp:   3,
	OutOfBed: 4,
}

// IsSleepEvent reports whether an event is one of the four the rules apply to.
// A correction to a noise or a light is stored without checking, because there
// is no ordering it could violate.
func IsSleepEvent(eventType int32) bool {
	_, ok := intendedOrder[eventType]
	return ok
}

// Record is one correction, as it will be stored.
//
// OldTime and NewTime are local wall clock "HH:MM", which is all the app sends
// and all the table keeps. They are not instants: turning them into instants is
// what Resolve does, and it needs the night and the offset to do it.
type Record struct {
	EventType int32
	DateNight time.Time // the night's local date
	OldTime   string
	NewTime   string
	IsCorrect bool
}

// The window a correction has to fall inside, as hour offsets from the night's
// own boundaries. A night starts at 20:00 and its data runs to noon two days
// later (hour 36).
const (
	nightStartHour   = 20
	nightDataEndHour = 36

	inBedSleepLowerOffset  = -2
	inBedSleepUpperOffset  = 4
	wakeOutOfBedLowerBound = 0
	wakeOutOfBedUpperBound = 4
)

// ErrOutsideWindow means the corrected time is one the night cannot contain.
var ErrOutsideWindow = fmt.Errorf("feedback: time outside the night's window")

// ErrOutOfOrder means the correction would put the four events in an
// impossible sequence.
var ErrOutOfOrder = fmt.Errorf("feedback: events out of order")

// Resolve turns a correction's local "HH:MM" into the instant it refers to.
//
// The window is what makes this fallible. A night that starts at 20:00 cannot
// contain a bedtime of 17:00, and rather than store a nonsense instant the
// reference refuses the whole correction. Returns ErrOutsideWindow when the
// hour falls in the dead zone between the end of one night's data and the start
// of the next.
//
// offsetMS shifts the window, and the caller's choice of what to pass is
// load-bearing in a way that is not obvious: see WindowOffsetForVerb.
func Resolve(r Record, offsetMS int32) (time.Time, error) {
	hour, minute, err := parseHourMinute(r.NewTime)
	if err != nil {
		return time.Time{}, err
	}

	// The night's boundaries as hours of the day, after the offset moves them.
	// Both can land anywhere, which is why the spans-midnight case exists.
	startHour := hourOfDay(nightStartHour, offsetMS)
	endHour := hourOfDay(nightDataEndHour, offsetMS)

	var lower, upper int
	switch r.EventType {
	case InBed, Sleep:
		lower, upper = startHour+inBedSleepLowerOffset, endHour+inBedSleepUpperOffset
	case WakeUp, OutOfBed:
		lower, upper = startHour+wakeOutOfBedLowerBound, endHour+wakeOutOfBedUpperBound
	default:
		// Not a sleep event: no window applies, and the time is taken as given
		// on the night's own date.
		return atLocalTime(r.DateNight, hour, minute, offsetMS), nil
	}

	nextDay := false
	if lower > upper {
		// The usual case: the window wraps past midnight, so an early hour
		// belongs to the following day and the gap between the two bounds is
		// the part of the afternoon no night can reach.
		switch {
		case hour >= 0 && hour < upper:
			nextDay = true
		case hour >= upper && hour < lower:
			return time.Time{}, ErrOutsideWindow
		}
	} else if hour >= upper || hour < lower {
		return time.Time{}, ErrOutsideWindow
	}

	date := r.DateNight
	if nextDay {
		date = date.AddDate(0, 0, 1)
	}
	return atLocalTime(date, hour, minute, offsetMS), nil
}

// WindowOffsetForVerb is the offset to hand Resolve, and it is not the same for
// every write.
//
// The reference passes a literal 0 when amending a time (PATCH) and the
// account's real offset when marking an event correct or incorrect (PUT and
// DELETE). Those produce different windows, so **the same correction is
// accepted by one verb and refused by another**.
//
// Verified against the running stack rather than inferred: marking a real
// GOT_IN_BED at 23:25 correct is refused with 412 "This adjustment could not be
// made because it is too early or too late", and nothing is written. With
// offset 0 the window is 18:00 to 16:00 across midnight, which accepts it; with
// -4 hours the window collapses to 00:00 to 20:00 on one day, and any bedtime
// at or after 20:00 falls outside it.
//
// This is a defect in the reference, and it is reproduced rather than fixed for
// the same reason as the others: the app is built against what this returns,
// and a version that accepts corrections the old one refused is a behaviour
// change wearing a bug fix's clothes. Fix it after the reference is gone, not
// while both are serving.
func WindowOffsetForVerb(method string) func(offsetMS int32) int32 {
	if strings.EqualFold(method, "PATCH") {
		return func(int32) int32 { return 0 }
	}
	return func(offsetMS int32) int32 { return offsetMS }
}

// CheckOrdering reports whether a proposed correction keeps the night's four
// events in a possible sequence, given the corrections already stored.
//
// Only the stored corrections are considered, not the algorithm's own events:
// the reference compares feedback against feedback. An empty history always
// passes, so the first correction of a night is never refused for ordering.
func CheckOrdering(existing []Record, proposed Record, offsetMS int32) error {
	if len(existing) == 0 {
		return nil
	}

	times := map[int32]time.Time{}
	for _, e := range existing {
		// A stored correction that no longer resolves is skipped rather than
		// failing the new one. It is already in the table and refusing an
		// unrelated edit because of it would leave the user unable to correct
		// anything for that night.
		if t, err := Resolve(e, offsetMS); err == nil {
			times[e.EventType] = t
		}
	}

	proposedTime, err := Resolve(proposed, offsetMS)
	if err != nil {
		return ErrOutsideWindow
	}
	times[proposed.EventType] = proposedTime

	// Walk the four in the order they must happen and require each to be
	// strictly later than the last. Strictly: two events at the same minute are
	// refused, because in bed and asleep at the same instant is not a night the
	// model should learn.
	var prev time.Time
	for order := 1; order <= 4; order++ {
		var t time.Time
		var found bool
		for eventType, o := range intendedOrder {
			if o == order {
				t, found = times[eventType]
				break
			}
		}
		if !found {
			continue
		}
		if !prev.IsZero() && !t.After(prev) {
			return ErrOutOfOrder
		}
		prev = t
	}
	return nil
}

// Validate runs both rules in the order the reference runs them, so the error
// the caller reports is the one the app would have seen.
func Validate(existing []Record, proposed Record, offsetMS int32) error {
	if !IsSleepEvent(proposed.EventType) {
		return nil
	}
	if _, err := Resolve(proposed, offsetMS); err != nil {
		return err
	}
	return CheckOrdering(existing, proposed, offsetMS)
}

// LocalHourMinute renders an instant as the "HH:MM" the table stores.
//
// The offset is added and the result read as UTC, which is the same local-UTC
// discipline the rest of the codebase uses: an instant that has had an offset
// applied is not in any zone any more.
func LocalHourMinute(ts time.Time, offsetMS int32) string {
	local := ts.UTC().Add(time.Duration(offsetMS) * time.Millisecond)
	return local.Format("15:04")
}

func parseHourMinute(s string) (hour, minute int, err error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("feedback: malformed time %q", s)
	}
	if hour, err = strconv.Atoi(parts[0]); err != nil {
		return 0, 0, fmt.Errorf("feedback: malformed hour in %q", s)
	}
	if minute, err = strconv.Atoi(parts[1]); err != nil {
		return 0, 0, fmt.Errorf("feedback: malformed minute in %q", s)
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("feedback: time out of range %q", s)
	}
	return hour, minute, nil
}

// hourOfDay is what hour a boundary lands on once the offset moves it. The
// offset is subtracted, matching the reference, and the result wraps.
func hourOfDay(boundaryHour int, offsetMS int32) int {
	shifted := boundaryHour - int(offsetMS)/3600000
	return ((shifted % 24) + 24) % 24
}

// atLocalTime builds the instant for a local wall clock on a date, undoing the
// offset to get back to UTC.
func atLocalTime(date time.Time, hour, minute int, offsetMS int32) time.Time {
	y, m, d := date.UTC().Date()
	local := time.Date(y, m, d, hour, minute, 0, 0, time.UTC)
	return local.Add(-time.Duration(offsetMS) * time.Millisecond)
}

// pairs maps each sleep event to the other end of the pair it is learned with.
//
// These pairings are NOT arbitrary and are not ours: they are the two output
// models ONLINE_HMM trains. OUTPUT_MODEL_SLEEP predicts falling asleep and
// waking up; OUTPUT_MODEL_BED predicts getting into and out of bed. LabelMaker
// builds one label set per output from whichever ends it was given.
var pairs = map[int32]int32{
	InBed:    OutOfBed,
	OutOfBed: InBed,
	Sleep:    WakeUp,
	WakeUp:   Sleep,
}

// PartnerOf returns the other end of an event's pair.
//
// Correcting one end alone is what collapses a model. LabelMaker has three
// branches per output: both ends give a full path, either end alone gives a
// one-sided one, and a one-sided label set can train an all-one-state model
// that decodes every night as a single state and yields NO transitions at all.
// The symptom is `transitions for <OUTPUT> are []`, then MISSING_KEY_EVENTS,
// then a silent permanent fall through to VOTING.
//
// That is not hypothetical. It happened to SLEEP on 2026-08-13 and to BED on
// 2026-08-15, and the second one went unnoticed for two days because the only
// symptom is that timelines quietly get worse. See
// knowledgebase/TIMELINE-ALGORITHMS.md.
func PartnerOf(eventType int32) (int32, bool) {
	partner, ok := pairs[eventType]
	return partner, ok
}

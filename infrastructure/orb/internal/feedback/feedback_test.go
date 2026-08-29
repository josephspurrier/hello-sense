package feedback

import (
	"errors"
	"testing"
	"time"
)

func night(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// The account the live checks were run against: US Eastern, four hours behind.
const easternOffsetMS int32 = -14400000

// TestVerbChangesWhetherBedtimeIsAccepted pins the reference's verb asymmetry.
//
// This is the finding that made the whole package worth having. The same
// correction, to the same event, at the same time, is accepted when the app
// amends the time and refused when the app marks it correct, because the two
// paths hand different offsets to the same window check.
//
// The refusal half was confirmed against the running Java stack: a PUT on a
// real GOT_IN_BED at 23:25 on 2026-08-13 returned 412 "This adjustment could
// not be made because it is too early or too late", and the feedback table was
// unchanged at nine rows before and after. If either half of this test starts
// failing, orb has stopped agreeing with the stack it is replacing.
func TestVerbChangesWhetherBedtimeIsAccepted(t *testing.T) {
	bedtime := Record{
		EventType: InBed,
		DateNight: night(2026, 8, 13),
		OldTime:   "23:25",
		NewTime:   "23:25",
		IsCorrect: true,
	}

	patchOffset := WindowOffsetForVerb("PATCH")(easternOffsetMS)
	if _, err := Resolve(bedtime, patchOffset); err != nil {
		t.Errorf("PATCH refused a 23:25 bedtime: %v, want accepted", err)
	}

	putOffset := WindowOffsetForVerb("PUT")(easternOffsetMS)
	if _, err := Resolve(bedtime, putOffset); !errors.Is(err, ErrOutsideWindow) {
		t.Errorf("PUT accepted a 23:25 bedtime (err %v), want ErrOutsideWindow", err)
	}
}

// A bedtime late in the evening belongs to the night's own date; an early
// morning wake belongs to the day after. Getting this backwards moves half a
// night onto the wrong date and is invisible until a timeline comes out empty.
func TestResolvePlacesEventsOnTheRightDay(t *testing.T) {
	date := night(2026, 8, 13)
	for _, c := range []struct {
		name      string
		eventType int32
		at        string
		wantDay   int
	}{
		{"bedtime stays on the night", InBed, "23:25", 13},
		{"falling asleep stays", Sleep, "23:40", 13},
		{"waking rolls over", WakeUp, "06:50", 14},
		{"getting up rolls over", OutOfBed, "06:51", 14},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := Resolve(Record{
				EventType: c.eventType, DateNight: date,
				OldTime: c.at, NewTime: c.at,
			}, 0)
			if err != nil {
				t.Fatalf("refused %s: %v", c.at, err)
			}
			if d := got.UTC().Day(); d != c.wantDay {
				t.Errorf("%s landed on day %d, want %d", c.at, d, c.wantDay)
			}
		})
	}
}

// The dead zone: with no offset, a night cannot contain a bedtime between 16:00
// and 18:00, and cannot contain a wake between 16:00 and 20:00. The two windows
// are deliberately different sizes.
func TestResolveRejectsTheDeadZone(t *testing.T) {
	date := night(2026, 8, 13)
	for _, c := range []struct {
		eventType int32
		at        string
		wantErr   bool
	}{
		{InBed, "17:00", true},
		{InBed, "18:30", false},
		{WakeUp, "17:00", true},
		{WakeUp, "19:00", true}, // still refused: the wake window opens at 20:00
		{WakeUp, "21:00", false},
	} {
		_, err := Resolve(Record{
			EventType: c.eventType, DateNight: date,
			OldTime: c.at, NewTime: c.at,
		}, 0)
		if gotErr := err != nil; gotErr != c.wantErr {
			t.Errorf("type %d at %s: err=%v, wantErr=%v", c.eventType, c.at, err, c.wantErr)
		}
	}
}

// Ordering is checked against the corrections already stored, and the first
// correction of a night can never fail it.
func TestCheckOrdering(t *testing.T) {
	date := night(2026, 8, 13)
	rec := func(et int32, at string) Record {
		return Record{EventType: et, DateNight: date, OldTime: at, NewTime: at}
	}

	if err := CheckOrdering(nil, rec(Sleep, "23:40"), 0); err != nil {
		t.Errorf("first correction of a night refused: %v", err)
	}

	existing := []Record{rec(InBed, "23:25"), rec(WakeUp, "06:50")}

	if err := CheckOrdering(existing, rec(Sleep, "23:40"), 0); err != nil {
		t.Errorf("in-order correction refused: %v", err)
	}
	// Falling asleep before getting into bed is not a night that happened.
	if err := CheckOrdering(existing, rec(Sleep, "23:00"), 0); !errors.Is(err, ErrOutOfOrder) {
		t.Errorf("sleep before in-bed gave %v, want ErrOutOfOrder", err)
	}
	// Getting up before waking is likewise impossible.
	if err := CheckOrdering(existing, rec(OutOfBed, "06:40"), 0); !errors.Is(err, ErrOutOfOrder) {
		t.Errorf("out-of-bed before wake gave %v, want ErrOutOfOrder", err)
	}
	// Same minute is refused too, not just earlier.
	if err := CheckOrdering(existing, rec(Sleep, "23:25"), 0); !errors.Is(err, ErrOutOfOrder) {
		t.Errorf("sleep at the bedtime minute gave %v, want ErrOutOfOrder", err)
	}
}

// Corrections to anything other than the four sleep events skip both rules.
// There is no ordering a noise could violate, and refusing one would leave the
// user unable to say a sound was wrong.
func TestNonSleepEventsSkipTheRules(t *testing.T) {
	const genericSound int32 = 6
	if IsSleepEvent(genericSound) {
		t.Fatal("generic sound counted as a sleep event")
	}
	err := Validate(nil, Record{
		EventType: genericSound, DateNight: night(2026, 8, 13),
		OldTime: "17:00", NewTime: "17:00", // inside the dead zone
	}, 0)
	if err != nil {
		t.Errorf("non-sleep correction refused: %v", err)
	}
}

// The URL segments the app sends, including the pair whose numbering does not
// match the order they happen in.
func TestEventTypeFromName(t *testing.T) {
	for name, want := range map[string]int32{
		"GOT_IN_BED": InBed, "FELL_ASLEEP": Sleep,
		"GOT_OUT_OF_BED": OutOfBed, "WOKE_UP": WakeUp,
	} {
		if got, ok := EventTypeFromName(name); !ok || got != want {
			t.Errorf("%s resolved to %d (ok=%v), want %d", name, got, ok, want)
		}
	}
	if _, ok := EventTypeFromName("NOT_AN_EVENT"); ok {
		t.Error("unknown event name accepted, want rejected")
	}
}

// LocalHourMinute has to agree with what the app saw on screen, which is the
// instant shifted by the account's offset and read as a wall clock.
func TestLocalHourMinute(t *testing.T) {
	// 1786677900000 is the real GOT_IN_BED the live check used: 23:25 Eastern.
	ts := time.UnixMilli(1786677900000)
	if got := LocalHourMinute(ts, easternOffsetMS); got != "23:25" {
		t.Errorf("got %s, want 23:25", got)
	}
}

// The pairing that stops a one-sided correction collapsing a model.
//
// The pairs are the two ONLINE_HMM output models, not an arbitrary grouping:
// SLEEP predicts falling asleep and waking, BED predicts getting in and out.
// Pairing them the other way round would train each model from one label of its
// own and one of the other's, which is worse than not pairing at all.
func TestPartnerOf(t *testing.T) {
	for _, c := range []struct {
		in, want int32
	}{
		{InBed, OutOfBed},
		{OutOfBed, InBed},
		{Sleep, WakeUp},
		{WakeUp, Sleep},
	} {
		got, ok := PartnerOf(c.in)
		if !ok || got != c.want {
			t.Errorf("PartnerOf(%d) = %d,%v; want %d,true", c.in, got, ok, c.want)
		}
	}

	// Anything that is not one of the four trains no model and must not be
	// paired: inventing a partner for a noise event would write a feedback row
	// about an event the person never saw.
	for _, other := range []int32{0, 1, 10, 15, 99} {
		if _, ok := PartnerOf(other); ok {
			t.Errorf("PartnerOf(%d) claimed a partner; want none", other)
		}
	}

	// Every pairing is symmetric. An asymmetric one would pair a correction
	// with an event that does not pair back, and one of the two models would
	// keep getting one-sided labels.
	for a, b := range pairs {
		if back, ok := PartnerOf(b); !ok || back != a {
			t.Errorf("pairing not symmetric: %d -> %d -> %d", a, b, back)
		}
	}
}

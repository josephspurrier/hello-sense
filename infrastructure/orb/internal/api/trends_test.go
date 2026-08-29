package api

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

// The reference's own aggregates for the five nights it had on 2026-08-15.
//
// Read out of the Java stack's DynamoDB table, not out of orb. That is the
// whole point of this test: orb's scoring model has since diverged from
// suripu's on three of these five nights, so a live diff of /v2/trends can
// never come back clean and cannot tell a rendering bug from a scoring
// difference. Feeding the reference's numbers through orb's rendering separates
// the two, and this is the only way the calendar logic gets checked at all.
//
// Divergent nights, for the record: 08-11 (orb 84/485), 08-12 (orb 72/405),
// 08-14 (orb 76/425). 08-10 and 08-13 agree exactly.
var referenceNights = []trendsStat{
	{date: day(2026, 8, 10), durationMins: 340, light: 0, medium: 93, sound: 232, score: 65},
	{date: day(2026, 8, 11), durationMins: 409, light: 0, medium: 63, sound: 328, score: 76},
	{date: day(2026, 8, 12), durationMins: 568, light: 0, medium: 249, sound: 148, score: 85},
	{date: day(2026, 8, 13), durationMins: 430, light: 0, medium: 148, sound: 253, score: 76},
	{date: day(2026, 8, 14), durationMins: 367, light: 0, medium: 181, sound: 169, score: 68},
}

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// The account the golden files were captured against: created 2026-07-27 local,
// so 20 days old on 2026-08-15, which is a Saturday.
var (
	goldenToday   = day(2026, 8, 15)
	goldenCreated = day(2026, 7, 27)
	goldenAge     = 20
)

// TestTrendsMatchesReference compares the whole rendered payload against
// responses captured from the running Java stack.
//
// Whole-payload rather than field-by-field on purpose: the bugs this endpoint
// invites are structural (a section boundary in the wrong place, a null where a
// -1 belongs), and an assertion that checks the values would have passed while
// the calendar was shifted by a day. The first run of this comparison is what
// caught exactly that.
func TestTrendsMatchesReference(t *testing.T) {
	for _, c := range []struct{ scale, file string }{
		{"LAST_WEEK", "testdata/trends_last_week.json"},
		{"LAST_MONTH", "testdata/trends_last_month.json"},
	} {
		t.Run(c.scale, func(t *testing.T) {
			scale, ok := timeScaleFrom(c.scale)
			if !ok {
				t.Fatalf("unknown scale %q", c.scale)
			}
			got := buildTrends(goldenAge, goldenCreated, goldenToday, scale, referenceNights)

			raw, err := os.ReadFile(c.file)
			if err != nil {
				t.Fatal(err)
			}
			var want, have any
			if err := json.Unmarshal(raw, &want); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &have); err != nil {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(want, have) {
				t.Errorf("payload differs\n got: %s\nwant: %s", encoded, raw)
			}
		})
	}
}

// A new account gets nothing at all, not an empty scale picker over three empty
// graphs. Three days is the boundary and it is inclusive.
func TestTrendsHidesGraphsForNewAccounts(t *testing.T) {
	scale, _ := timeScaleFrom("LAST_WEEK")
	got := buildTrends(minAccountAge, goldenCreated, goldenToday, scale, referenceNights)
	if len(got.Graphs) != 0 || len(got.AvailableTimeScales) != 0 {
		t.Errorf("age %d returned %d graphs and %d scales, want none",
			minAccountAge, len(got.Graphs), len(got.AvailableTimeScales))
	}
}

// Under three nights there are no graphs, but the scale picker stays: the
// account is established, it just has not slept enough for a trend.
func TestTrendsNeedsThreeNights(t *testing.T) {
	scale, _ := timeScaleFrom("LAST_WEEK")
	got := buildTrends(goldenAge, goldenCreated, goldenToday, scale, referenceNights[:2])
	if len(got.Graphs) != 0 {
		t.Errorf("two nights produced %d graphs, want 0", len(got.Graphs))
	}
	if len(got.AvailableTimeScales) == 0 {
		t.Error("two nights dropped the time scales, want them kept")
	}
}

// All three annotations or none. Every night here is a weekday, so the weekend
// average cannot be computed and the reference drops the whole set rather than
// showing two of three. This is why a real response carries an empty
// annotations list on an account well past the age gate.
func TestTrendsAnnotationsAreAllOrNothing(t *testing.T) {
	scale, _ := timeScaleFrom("LAST_WEEK")
	got := buildTrends(goldenAge, goldenCreated, goldenToday, scale, referenceNights)
	for _, g := range got.Graphs {
		if len(g.Annotations) != 0 {
			t.Errorf("%s: got %d annotations from weekdays only, want 0",
				g.Title, len(g.Annotations))
		}
	}
}

// With a weekend night present all three appear, which is the other half of the
// rule above: the empty list must come from the missing weekend, not from
// annotations being broken outright.
func TestTrendsAnnotationsAppearWithAWeekend(t *testing.T) {
	scale, _ := timeScaleFrom("LAST_WEEK")
	// 2026-08-09 is a Sunday.
	withWeekend := append([]trendsStat{
		{date: day(2026, 8, 9), durationMins: 400, light: 0, medium: 100, sound: 300, score: 70},
	}, referenceNights...)

	got := buildTrends(goldenAge, goldenCreated, goldenToday, scale, withWeekend)
	if len(got.Graphs[0].Annotations) != 3 {
		t.Fatalf("got %d annotations, want 3", len(got.Graphs[0].Annotations))
	}
	if c := got.Graphs[0].Annotations[0].Condition; c == nil {
		t.Error("score annotation has no condition, want one")
	}
	// Duration annotations carry no condition: there is no such thing as an
	// ideal number of hours in this model.
	if c := got.Graphs[1].Annotations[0].Condition; c != nil {
		t.Errorf("duration annotation has condition %q, want none", *c)
	}
}

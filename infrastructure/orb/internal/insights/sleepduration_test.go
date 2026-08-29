package insights

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The recommendation table, pinned against the reference's own switch.
//
// Every branch, and both sides of every boundary it turns on. This is the
// ported half of the generator, so a transcription slip here is a silent
// disagreement with the sleep score, which reads the same table through the
// algorithm service.
func TestRecommendationTableMatchesTheReference(t *testing.T) {
	for _, c := range []struct {
		age  int
		want recommendation
		why  string
	}{
		{0, recommendation{7, 9, 6, 10}, "no birthdate: assume an adult"},
		{1, recommendation{10, 13, 8, 14}, "preschooler"},
		{5, recommendation{10, 13, 8, 14}, "still preschool at 5"},
		{6, recommendation{9, 11, 7, 12}, "school age begins"},
		{13, recommendation{9, 11, 7, 12}, "still school age at 13"},
		{14, recommendation{8, 10, 7, 11}, "teenager begins"},
		{17, recommendation{8, 10, 7, 11}, "still a teenager at 17"},
		{18, recommendation{7, 9, 6, 11}, "young adult begins"},
		{25, recommendation{7, 9, 6, 11}, "still a young adult at 25"},
		{26, recommendation{7, 9, 6, 10}, "adult begins"},
		{38, recommendation{7, 9, 6, 10}, "this account"},
		{64, recommendation{7, 9, 6, 10}, "still an adult at 64"},
		{65, recommendation{7, 8, 5, 9}, "older adult begins"},
		{90, recommendation{7, 8, 5, 9}, "older adult"},
	} {
		if got := sleepDurationRecommendation(c.age); got != c.want {
			t.Errorf("age %d (%s) = %+v, want %+v", c.age, c.why, got, c.want)
		}
	}
}

// Age 0 means "no birthdate", not "newborn".
//
// It is the first test in the reference's switch and the last one anybody would
// think to write, because the bug it prevents is invisible: a blank profile
// falling through to the preschooler branch produces a perfectly well-formed
// card telling an adult to sleep thirteen hours.
func TestUnknownAgeIsAnAdultNotANewborn(t *testing.T) {
	unknown := sleepDurationRecommendation(0)
	if unknown != sleepDurationRecommendation(30) {
		t.Errorf("age 0 = %+v, want the adult band %+v",
			unknown, sleepDurationRecommendation(30))
	}
	if unknown == sleepDurationRecommendation(1) {
		t.Error("age 0 fell through to the preschooler band")
	}
}

// The five bands, at every boundary, for the adult recommendation (7 to 9,
// absolute 6 to 10).
//
// The recommended range is INCLUSIVE on both sides. Exactly seven hours is well
// rested, and getting that wrong tells somebody who hit their target that they
// missed it.
func TestSleepDurationBands(t *testing.T) {
	adult := recommendation{7, 9, 6, 10}
	for _, c := range []struct {
		minutes float64
		title   string
	}{
		{5 * 60, "Hello, running on empty"},
		{6*60 - 1, "Hello, running on empty"},
		{6 * 60, "Hello, a little short"},
		{7*60 - 1, "Hello, a little short"},
		{7 * 60, "Hello, well rested"}, // exactly the minimum is in range
		{432, "Hello, well rested"},    // this account's current average
		{9 * 60, "Hello, well rested"}, // exactly the maximum is in range
		{9*60 + 1, "Hello, long sleeper"},
		{10 * 60, "Hello, long sleeper"}, // exactly the outer bound is still long
		{10*60 + 1, "Hello, very long sleeper"},
	} {
		title, _ := sleepDurationText(c.minutes, 7, adult)
		if title != c.title {
			t.Errorf("%.0f minutes = %q, want %q", c.minutes, title, c.title)
		}
	}
}

// The quoted shortfall has to be a positive number in the direction the
// sentence already names, or the card reads "you are about -1.2 hours short".
func TestGapIsQuotedPositively(t *testing.T) {
	adult := recommendation{7, 9, 6, 10}
	for _, minutes := range []float64{4 * 60, 5 * 60, 6*60 + 30, 9*60 + 30, 11 * 60} {
		_, msg := sleepDurationText(minutes, 7, adult)
		if strings.Contains(msg, "-") {
			t.Errorf("%.0f minutes: negative number in %q", minutes, msg)
		}
	}
}

// Every band names the recommended range and the average, because a card that
// says only "you slept a bit short" gives nobody anything to act on.
func TestEveryBandQuotesTheNumbers(t *testing.T) {
	adult := recommendation{7, 9, 6, 10}
	for _, minutes := range []float64{5 * 60, 6*60 + 30, 8 * 60, 9*60 + 30, 11 * 60} {
		title, msg := sleepDurationText(minutes, 5, adult)
		if title == "" {
			t.Fatalf("%.0f minutes: no title", minutes)
		}
		for _, want := range []string{"7 to 9", "over the last 5 nights", "**"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%.0f minutes: missing %q in %q", minutes, want, msg)
			}
		}
	}
}

// The gap is quoted in a unit the data supports.
//
// Regression test for the first card the generator ever wrote in production:
// "you are about 0.1 hours short of it", off four nights ranging 5.3 to 9.7
// hours. Six minutes, to a tenth of an hour.
func TestGapUnits(t *testing.T) {
	for _, c := range []struct {
		gapHours float64
		want     string
		ok       bool
		why      string
	}{
		{0, "", false, "no gap at all"},
		{0.1, "", false, "six minutes: the bug"},
		{14.0 / 60, "", false, "just under the threshold"},
		{15.0 / 60, "15 minutes", true, "exactly the threshold"},
		{0.5, "30 minutes", true, "half an hour reads as minutes"},
		{59.0 / 60, "59 minutes", true, "just under an hour"},
		{1, "1.0 hours", true, "an hour reads as hours"},
		{2.5, "2.5 hours", true, "well over"},
	} {
		got, ok := gapPhrase(c.gapHours)
		if got != c.want || ok != c.ok {
			t.Errorf("gapPhrase(%.3f) = (%q, %v), want (%q, %v) [%s]",
				c.gapHours, got, ok, c.want, c.ok, c.why)
		}
	}
}

// A card must never quote a GAP in tenths of an hour when the gap is under an
// hour, which is the shape of the original defect regardless of which band
// produces it.
//
// The average itself is always a decimal hours figure and is always the bolded
// span, so it is cut out before scanning. Without that, an average of exactly
// 10.0 hours trips the check and the test reports a bug that is not there.
func TestNoCardQuotesASmallGapInHours(t *testing.T) {
	adult := recommendation{7, 9, 6, 10}
	bolded := regexp.MustCompile(`\*\*[^*]*\*\*`)

	// Sweep both bands adjacent to the recommended range, minute by minute.
	for m := 6 * 60; m <= 10*60; m++ {
		_, msg := sleepDurationText(float64(m), 4, adult)
		rest := bolded.ReplaceAllString(msg, "")
		for _, bad := range []string{"0.0 hours", "0.1 hours", "0.2 hours",
			"0.3 hours", "0.4 hours", "0.5 hours", "0.6 hours", "0.7 hours",
			"0.8 hours", "0.9 hours"} {
			if strings.Contains(rest, bad) {
				t.Fatalf("%d minutes quotes a gap of %q: %q", m, bad, msg)
			}
		}
	}
}

// The exact card production wrote on 2026-08-26, now reworded.
func TestTheCardThatExposedTheDefect(t *testing.T) {
	nights := []int{320, 375, 383, 580} // 08-20, 08-23, 08-24, 08-25
	title, msg := sleepDurationText(meanMinutes(nights), len(nights),
		recommendation{7, 9, 6, 10})

	if title != "Hello, a little short" {
		t.Errorf("title = %q", title)
	}
	if !strings.Contains(msg, "**6.9 hours**") {
		t.Errorf("lost the average: %q", msg)
	}
	if !strings.Contains(msg, "just under") {
		t.Errorf("want the no-gap wording, got %q", msg)
	}
	if strings.Contains(msg, "0.1") {
		t.Errorf("still quoting a six-minute shortfall: %q", msg)
	}
}

func TestGenerateNeedsThreeNights(t *testing.T) {
	g := SleepDurationGenerator{}
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

	for _, nights := range [][]int{nil, {430}, {430, 450}} {
		card, err := g.Generate(context.Background(), fakeData{sleep: nights, age: 38}, 1, now)
		if err != nil {
			t.Fatalf("%d nights: %v", len(nights), err)
		}
		if card != nil {
			t.Errorf("%d nights produced a card: %q", len(nights), card.Title)
		}
	}

	card, err := g.Generate(context.Background(),
		fakeData{sleep: []int{430, 450, 425}, age: 38}, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if card == nil {
		t.Fatal("three nights produced no card")
	}
	if card.Category != "SLEEP_DURATION" {
		t.Errorf("category = %q", card.Category)
	}
	if !card.Timestamp.Equal(now) {
		t.Errorf("timestamp = %v, want %v", card.Timestamp, now)
	}
}

// This account's real week, from sleep_stats on 2026-08-20. It should read as
// well rested, and if a change to the bands ever moves it, that is worth
// finding out from a test rather than from the feed.
func TestThisAccountsRealWeek(t *testing.T) {
	week := []int{450, 461, 380, 425, 515} // 08-15 through 08-19
	card, err := SleepDurationGenerator{}.Generate(context.Background(),
		fakeData{sleep: week, age: 38}, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if card == nil {
		t.Fatal("no card")
	}
	if card.Title != "Hello, well rested" {
		t.Errorf("title = %q, want %q", card.Title, "Hello, well rested")
	}
	// mean is 446.2 minutes, which is 7.4 hours
	if !strings.Contains(card.Message, "**7.4 hours**") {
		t.Errorf("message does not quote 7.4 hours: %q", card.Message)
	}
}

// A store error has to reach the worker, which logs it and moves on. Swallowing
// it would produce a card built on an empty series instead.
func TestStoreErrorsPropagate(t *testing.T) {
	boom := errors.New("boom")
	_, err := SleepDurationGenerator{}.Generate(context.Background(),
		fakeData{sleep: []int{430, 450, 425}, err: boom}, 1, time.Now())
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

func TestCadenceIsWeekly(t *testing.T) {
	if got := (SleepDurationGenerator{}).Every(); got != 7*24*time.Hour {
		t.Errorf("Every() = %v, want a week", got)
	}
}

// The registry has to list the generator, or every test above passes and no
// card is ever written. Also pins that the categories have art, which the api
// package tests from the other side.
func TestRegistered(t *testing.T) {
	var found bool
	for _, g := range All() {
		if g.Category() == "SLEEP_DURATION" {
			found = true
		}
	}
	if !found {
		t.Error("SLEEP_DURATION is not in All()")
	}
}

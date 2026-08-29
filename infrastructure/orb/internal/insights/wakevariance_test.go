package insights

import (
	"context"
	"strings"
	"testing"
	"time"
)

// fakeData serves every generator in the package. `minutes` is the wake-time
// series and `sleep` the duration series; a test sets only the one its
// generator reads, and the other stays nil, which is exactly the "not enough
// nights" case the generators are supposed to decline.
type fakeData struct {
	minutes []int
	sleep   []int
	age     int
	err     error
}

func (f fakeData) WakeMinutes(context.Context, int64, time.Time, time.Time) ([]int, error) {
	return f.minutes, f.err
}

func (f fakeData) SleepMinutes(context.Context, int64, time.Time, time.Time) ([]int, error) {
	return f.sleep, f.err
}

func (f fakeData) AgeYears(context.Context, int64, time.Time) (int, error) {
	return f.age, f.err
}

// TestPercentileTableMatchesTheRealCard pins the generated distribution table
// against the one card the reference has actually produced for this account.
//
// That card reports a deviation of 2.6 hours, which is 156 minutes, at the 97th
// percentile. If the table were transcribed with rounding instead of the
// reference's truncation, this would come out at 97 or 98 depending on the row
// and the error would be invisible in every other test.
//
// The three band boundaries are checked against the percentiles the reference's
// own comments claim for them, which is an independent statement of the same
// table and catches an off-by-one across the whole thing.
func TestPercentileTableMatchesTheRealCard(t *testing.T) {
	if got := percentileFor(156); got != 97 {
		t.Errorf("percentileFor(156) = %d, want 97 (from the live WAKE_VARIANCE card)", got)
	}
	for _, c := range []struct{ stdDev, want int }{
		{50, 24},  // reference comment: "25 percentile"
		{79, 50},  // "50 percentile"
		{108, 75}, // "75 percentile"
		{169, 99}, // the cap
		{500, 99}, // above the cap
		{0, 0},
	} {
		if got := percentileFor(c.stdDev); got != c.want {
			t.Errorf("percentileFor(%d) = %d, want %d", c.stdDev, got, c.want)
		}
	}
}

// The four bands, at their boundaries. Each boundary is inclusive of the lower
// band, which is what decides the adjective a person reads about themselves.
func TestWakeVarianceBands(t *testing.T) {
	for _, c := range []struct {
		stdDev int
		title  string
	}{
		{0, "Hello, very regular"},
		{50, "Hello, very regular"},
		{51, "Hello, regular"},
		{79, "Hello, regular"},
		{80, "Hello, irregular"},
		{108, "Hello, irregular"},
		{109, "Hello, very irregular"},
		{156, "Hello, very irregular"},
	} {
		title, _ := wakeVarianceText(c.stdDev, percentileFor(c.stdDev))
		if title != c.title {
			t.Errorf("stdDev %d gave %q, want %q", c.stdDev, title, c.title)
		}
	}
}

// The comparison inverts between the consistent and inconsistent halves: the
// good bands say "more consistent than 100-percentile", the bad ones say "less
// consistent than percentile". Getting it backwards tells a regular sleeper
// they are worse than nearly everybody.
func TestWakeVarianceComparisonInverts(t *testing.T) {
	_, good := wakeVarianceText(30, percentileFor(30))
	if !strings.Contains(good, "more consistent than") {
		t.Errorf("consistent band should say 'more consistent than': %s", good)
	}
	_, bad := wakeVarianceText(156, percentileFor(156))
	if !strings.Contains(bad, "less consistent than 97%") {
		t.Errorf("inconsistent band should say 'less consistent than 97%%': %s", bad)
	}
}

// The exact wording of the card the reference produced, reproduced from the
// same inputs. The trailing space before the blank line is in the original and
// is deliberate; the migrated cards have it.
func TestWakeVarianceWordingMatchesTheStoredCard(t *testing.T) {
	title, msg := wakeVarianceText(156, 97)
	if title != "Hello, very irregular" {
		t.Errorf("title = %q", title)
	}
	want := "The time you wake up each morning is **pretty inconsistent**. " +
		"It varied an average of 2.6 hours last week, which is less consistent " +
		"than 97% of other people using Sense. \n\nWaking up at the same time " +
		"each morning is great for your internal clock, and helps you sleep better."
	if msg != want {
		t.Errorf("message differs\n got: %q\nwant: %q", msg, want)
	}
}

// Sample standard deviation, not population. The reference uses Apache Commons'
// DescriptiveStatistics, which divides by n-1.
func TestSampleStdDev(t *testing.T) {
	// The account's five real wake minutes, which give 135.3 by the sample
	// estimator and 121.0 by the population one.
	got := sampleStdDev([]int{330, 464, 601, 410, 658})
	if got < 135.2 || got > 135.4 {
		t.Errorf("sampleStdDev = %.2f, want ~135.3 (sample, not population)", got)
	}
}

// Fewer than three nights produces nothing at all. A deviation over two points
// is not a description of a habit, and the reference refuses it too.
func TestWakeVarianceNeedsThreeNights(t *testing.T) {
	g := WakeVarianceGenerator{}
	now := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	for _, n := range [][]int{nil, {400}, {400, 500}} {
		card, err := g.Generate(context.Background(), fakeData{minutes: n}, 1, now)
		if err != nil {
			t.Fatal(err)
		}
		if card != nil {
			t.Errorf("%d nights produced a card, want none", len(n))
		}
	}
	card, err := g.Generate(context.Background(), fakeData{minutes: []int{400, 500, 600}}, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if card == nil {
		t.Fatal("three nights produced no card")
	}
	if card.Category != "WAKE_VARIANCE" {
		t.Errorf("category = %q", card.Category)
	}
}

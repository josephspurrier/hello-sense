package insights

import (
	"context"
	"fmt"
	"math"
	"time"
)

// WakeVarianceGenerator reports how consistent the account's wake time is.
//
// This is the one insight the reference has actually produced for this account
// beyond the welcome card, which is why it is the one that got built first.
type WakeVarianceGenerator struct{}

func (WakeVarianceGenerator) Category() string { return "WAKE_VARIANCE" }

// Every is a week. The message says "last week" in so many words, so producing
// one more often would make the card contradict itself.
func (WakeVarianceGenerator) Every() time.Duration { return 7 * 24 * time.Hour }

// wakeVarianceDays is the window the deviation is measured over.
const wakeVarianceDays = 7

// minWakeNights is the reference's rule: three or more nights, because a
// standard deviation over two points is not a description of a habit.
const minWakeNights = 3

// The band boundaries, in minutes of standard deviation. From the reference,
// where they are annotated with the percentile each corresponds to: 50 is the
// 25th percentile, 79 the 50th, 108 the 75th.
const (
	wakeVeryConsistent = 50
	wakeConsistent     = 79
	wakeInconsistent   = 108
)

func (g WakeVarianceGenerator) Generate(ctx context.Context, d Data, accountID int64, now time.Time) (*Card, error) {
	minutes, err := d.WakeMinutes(ctx, accountID, now.AddDate(0, 0, -wakeVarianceDays), now)
	if err != nil {
		return nil, err
	}
	if len(minutes) < minWakeNights {
		return nil, nil
	}

	stdDev := int(math.Round(sampleStdDev(minutes)))
	percentile := percentileFor(stdDev)
	title, body := wakeVarianceText(stdDev, percentile)

	return &Card{
		Category: g.Category(), Title: title, Message: body, Timestamp: now,
	}, nil
}

// sampleStdDev is the SAMPLE standard deviation, dividing by n-1.
//
// Apache Commons' DescriptiveStatistics.getStandardDeviation, which the
// reference uses, is the sample estimator. The population one divides by n and
// gives a visibly smaller number on a week of data, which would move people
// into a calmer band than they belong in.
func sampleStdDev(values []int) float64 {
	if len(values) < 2 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += float64(v)
	}
	mean := sum / float64(len(values))

	var sq float64
	for _, v := range values {
		d := float64(v) - mean
		sq += d * d
	}
	return math.Sqrt(sq / float64(len(values)-1))
}

// wakeVarianceText renders the card, matching the reference's wording exactly.
//
// The four variants differ in the adjective, the title, and whether the
// comparison is "more consistent than 100-percentile" or "less consistent than
// percentile". Getting that inversion wrong tells a regular sleeper they are
// worse than almost everybody.
//
// The trailing space before the blank line is in the original and is kept: the
// stored cards have it, so removing it would make every regenerated card differ
// from every migrated one.
func wakeVarianceText(stdDev, percentile int) (title, message string) {
	hours := float64(stdDev) / 60.0
	const advice = "\n\nWaking up at the same time each morning is great for your internal clock, and helps you sleep better."

	switch {
	case stdDev <= wakeVeryConsistent:
		return "Hello, very regular", fmt.Sprintf(
			"The time you wake up each morning is **very consistent**. It varied an average of %.1f hours last week, "+
				"which is more consistent than %d%% of other people using Sense. ", hours, 100-percentile) + advice
	case stdDev <= wakeConsistent:
		return "Hello, regular", fmt.Sprintf(
			"The time you wake up each morning is **fairly consistent**. It varied an average of %.1f hours last week, "+
				"which is more consistent than %d%% of other people using Sense. ", hours, 100-percentile) + advice
	case stdDev <= wakeInconsistent:
		return "Hello, irregular", fmt.Sprintf(
			"The time you wake up each morning is **a little inconsistent**. It varied an average of %.1f hours last week, "+
				"which is less consistent than %d%% of other people using Sense. ", hours, percentile) + advice
	default:
		return "Hello, very irregular", fmt.Sprintf(
			"The time you wake up each morning is **pretty inconsistent**. It varied an average of %.1f hours last week, "+
				"which is less consistent than %d%% of other people using Sense. ", hours, percentile) + advice
	}
}

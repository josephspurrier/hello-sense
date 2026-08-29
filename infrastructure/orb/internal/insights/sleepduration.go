package insights

import (
	"context"
	"fmt"
	"math"
	"time"
)

// SleepDurationGenerator reports how much the account slept against the amount
// recommended for its age.
//
// PART PORT, PART REVIVAL, and it matters which half is which.
//
// The recommendation table below is a straight port of the reference's
// SleepDuration.getSleepDurationRecommendation, which is the numerically
// load-bearing half: it decides the bands, it is cited to a published expert
// panel, and the same table already drives orb's sleep score through the
// algorithm service. Changing it here would put this card and the score it sits
// next to into disagreement.
//
// The card itself is NEW. The reference never shipped a recurring sleep
// duration insight; SLEEP_DURATION exists there only as the category on a
// static welcome tip, and the table is consumed by the score and by
// SleepDeprivation rather than by any card. So there is no original wording to
// match and none is claimed. The tone follows SleepDeprivationMsgEN, which is
// the nearest real data card: a short title, then two sentences, one stating
// the number and one saying why it matters.
//
// Weekly, unlike a card about the thermostat. The distinction is whether the
// number can move on its own. A room held at a set temperature reports the same
// thing every week and a card about it is a nag; a week's sleep is genuinely
// different information each time, even when the band it lands in is the same.
type SleepDurationGenerator struct{}

func (SleepDurationGenerator) Category() string { return "SLEEP_DURATION" }

// Every is a week, and the message says "over the last N nights" rather than
// "last week" so that a card written from four nights does not overclaim.
func (SleepDurationGenerator) Every() time.Duration { return 7 * 24 * time.Hour }

// sleepDurationDays is the window the average is taken over.
const sleepDurationDays = 7

// minSleepNights matches the wake variance rule and exists for the same reason:
// a mean over two nights describes two nights, not a habit. One bad night in a
// pair moves the average by hours and would put a settled sleeper in the
// "well short" band.
const minSleepNights = 3

// recommendation is the reference's nested class of the same name.
type recommendation struct {
	minHours         int
	maxHours         int
	absoluteMinHours int
	absoluteMaxHours int
}

// sleepDurationRecommendation ports getSleepDurationRecommendation verbatim,
// including the order of the tests, which is load-bearing: age 0 is checked
// FIRST and means "no birthdate on file", so it must not fall through to the
// preschooler branch at the bottom and tell an adult with a blank profile that
// they should be getting thirteen hours.
//
// Source, cited in the reference's own comment:
// http://www.prnewswire.com/news-releases/expert-panel-recommends-new-sleep-durations-300028815.html
func sleepDurationRecommendation(ageYears int) recommendation {
	switch {
	case ageYears == 0: // no DOB, assume it's an adult
		return recommendation{7, 9, 6, 10}
	case ageYears >= 65: // older adults
		return recommendation{7, 8, 5, 9}
	case ageYears >= 26: // adults
		return recommendation{7, 9, 6, 10}
	case ageYears >= 18: // young adults
		return recommendation{7, 9, 6, 11}
	case ageYears >= 14: // teenagers
		return recommendation{8, 10, 7, 11}
	case ageYears >= 6: // school age children
		return recommendation{9, 11, 7, 12}
	default: // preschoolers
		return recommendation{10, 13, 8, 14}
	}
}

func (g SleepDurationGenerator) Generate(ctx context.Context, d Data, accountID int64, now time.Time) (*Card, error) {
	minutes, err := d.SleepMinutes(ctx, accountID, now.AddDate(0, 0, -sleepDurationDays), now)
	if err != nil {
		return nil, err
	}
	if len(minutes) < minSleepNights {
		return nil, nil
	}

	// Age on the day the card is written, not on the first night of the window.
	// The two differ only for someone whose birthday fell mid-week, and then
	// the newer answer is the right one to quote at them.
	age, err := d.AgeYears(ctx, accountID, now)
	if err != nil {
		return nil, err
	}

	title, body := sleepDurationText(meanMinutes(minutes), len(minutes),
		sleepDurationRecommendation(age))

	return &Card{
		Category: g.Category(), Title: title, Message: body, Timestamp: now,
	}, nil
}

func meanMinutes(values []int) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum int
	for _, v := range values {
		sum += v
	}
	return float64(sum) / float64(len(values))
}

// gapPhrase renders a shortfall or an excess in a unit that does not overclaim,
// and reports whether it is worth stating at all.
//
// This exists because the first card the generator ever wrote said "you are
// about 0.1 hours short of it". Six minutes, quoted to a tenth of an hour, off a
// mean of four nights that ranged from 5.3 to 9.7 hours. The arithmetic was
// right and the sentence was indefensible: nothing in that data supports a claim
// at that precision, and a card fussing over six minutes teaches people to stop
// reading cards.
//
// So: under a quarter of an hour there is no gap worth naming and the sentence
// says only which side of the range the average sits on. Under an hour it is
// whole minutes, which is the unit a person thinks in at that size. Above that,
// hours.
func gapPhrase(gapHours float64) (string, bool) {
	minutes := int(math.Round(gapHours * 60))
	switch {
	case minutes < 15:
		return "", false
	case minutes < 60:
		return fmt.Sprintf("%d minutes", minutes), true
	default:
		return fmt.Sprintf("%.1f hours", gapHours), true
	}
}

// sleepDurationText renders the card.
//
// Five bands, from the four boundaries the recommendation carries. The inner
// pair is the recommended range and the outer pair is where the reference stops
// treating a duration as a variation and starts treating it as a problem.
//
// The boundaries are INCLUSIVE of the recommended range on both sides: exactly
// seven hours is in range, not short. Getting that backwards would tell someone
// who hit their target precisely that they missed it.
//
// Only the two bands ADJACENT to the recommended range can produce a small gap,
// because the outer two start a full hour beyond it. That is why only those two
// have a no-gap variant.
func sleepDurationText(avgMinutes float64, nights int, rec recommendation) (title, message string) {
	hours := avgMinutes / 60.0
	const advice = "\n\nHow long you sleep is the part of a night you have the most control over, and it moves your score more than anything else in the room."

	lead := fmt.Sprintf("You averaged **%.1f hours** of sleep over the last %d nights",
		hours, nights)
	band := fmt.Sprintf("the %d to %d hours recommended for your age",
		rec.minHours, rec.maxHours)

	switch {
	case hours < float64(rec.absoluteMinHours):
		gap, _ := gapPhrase(float64(rec.minHours) - hours)
		return "Hello, running on empty", fmt.Sprintf(
			"%s, well under %s. That is a shortfall of about %s a night.",
			lead, band, gap) + advice

	case hours < float64(rec.minHours):
		if gap, ok := gapPhrase(float64(rec.minHours) - hours); ok {
			return "Hello, a little short", fmt.Sprintf(
				"%s, about %s under %s.", lead, gap, band) + advice
		}
		return "Hello, a little short", fmt.Sprintf(
			"%s, which is just under %s.", lead, band) + advice

	case hours <= float64(rec.maxHours):
		return "Hello, well rested", fmt.Sprintf(
			"%s, which is inside %s. That is the range worth defending.",
			lead, band) + advice

	case hours <= float64(rec.absoluteMaxHours):
		if gap, ok := gapPhrase(hours - float64(rec.maxHours)); ok {
			return "Hello, long sleeper", fmt.Sprintf(
				"%s, about %s more than %s. A long week is usually a week catching up on a short one.",
				lead, gap, band) + advice
		}
		return "Hello, long sleeper", fmt.Sprintf(
			"%s, which is just over %s. A long week is usually a week catching up on a short one.",
			lead, band) + advice

	default:
		return "Hello, very long sleeper", fmt.Sprintf(
			"%s, well over %s. Consistently sleeping this much is worth mentioning to a doctor if it is new.",
			lead, band) + advice
	}
}

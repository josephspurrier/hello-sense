// Package insights generates the cards on the app's insights feed.
//
// This is a DELIBERATELY SIMPLIFIED generator, not a port, and the reason is
// recorded in the knowledgebase: an insight card is a snapshot of data as it
// was, so a card written last week cannot be regenerated from this week's
// database. The reference's own WAKE_VARIANCE card quotes a figure that its own
// current data no longer produces. That makes the usual method here, diffing
// against the running stack, unusable for this one feature.
//
// So these are verified by their own tests instead: the band boundaries and the
// message templates are pinned, and the percentile table is generated from the
// reference's embedded distribution rather than transcribed. What can be
// checked is which card is produced and how it is worded. What cannot be
// checked is the number in it, because that comes from data that both stacks
// keep recomputing.
//
// The reference picks a category per run through a weekly slot, a high-priority
// list, a random slot and a one-time slot, each behind a feature flag. This
// offers every generator a turn in order and stores AT MOST ONE card per
// account per pass, which is the part of that design that matters: a feed that
// arrives several cards at a time is one people stop reading.
//
// Generators do not starve each other, because due-ness is decided from what is
// already stored. A generator that fires is not due again for its own interval,
// so on the next pass the turn falls to the next one that is due. Order here
// only decides which of two simultaneously-due generators goes first; the other
// follows one InsightInterval later.
package insights

import (
	"context"
	"time"
)

// Card is a generated insight, ready to store.
type Card struct {
	Category  string
	Title     string
	Message   string
	Timestamp time.Time
}

// Data is what a generator may read. Narrow on purpose: a generator that can
// reach the whole store will eventually query something expensive on a timer.
type Data interface {
	// WakeMinutes returns the local wake time, as minutes past midnight, for
	// each scored night in the window, oldest first.
	WakeMinutes(ctx context.Context, accountID int64, from, to time.Time) ([]int, error)
	// SleepMinutes returns the sleep duration of each scored night in the
	// window, oldest first. Same window convention as WakeMinutes.
	SleepMinutes(ctx context.Context, accountID int64, from, to time.Time) ([]int, error)
	// AgeYears is the account's age in whole years on a date, or 0 when no
	// birthdate is on file. Zero means UNKNOWN, not newborn.
	AgeYears(ctx context.Context, accountID int64, on time.Time) (int, error)
}

// Generator produces at most one card for an account.
//
// Returning (nil, nil) is the normal outcome and is not an error: most
// generators most of the time have nothing worth saying, and the feed is better
// for it.
type Generator interface {
	// Category is the insight category this generator produces. Used to decide
	// when it last ran.
	Category() string
	// Every is how long to wait between cards from this generator.
	Every() time.Duration
	// Generate returns a card, or nil when there is nothing to say.
	Generate(ctx context.Context, d Data, accountID int64, now time.Time) (*Card, error)
}

// All is the set of generators that run, in the order they are offered a turn.
//
// Wake variance goes first because it is the only one that has ever told this
// account something it did not already know. Sleep duration mostly confirms
// that a normal week was normal, which is worth saying and worth saying second.
func All() []Generator {
	return []Generator{WakeVarianceGenerator{}, SleepDurationGenerator{}}
}

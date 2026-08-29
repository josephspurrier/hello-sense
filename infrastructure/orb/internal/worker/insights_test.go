package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/josephspurrier/hello-orb/orb/internal/insights"
)

// A generator that always has something to say, so the pass is bounded by the
// one-card rule rather than by anything running out of data.
type alwaysGen struct {
	category string
	every    time.Duration
	fail     bool
	silent   bool
	// unstamped returns a card with no Timestamp, the way a newly written
	// generator that forgot the field would.
	unstamped bool
}

func (g alwaysGen) Category() string { return g.category }

func (g alwaysGen) Every() time.Duration {
	if g.every == 0 {
		return 7 * 24 * time.Hour
	}
	return g.every
}

func (g alwaysGen) Generate(_ context.Context, _ insights.Data, _ int64, now time.Time) (*insights.Card, error) {
	if g.fail {
		return nil, errors.New("generator exploded")
	}
	if g.silent {
		return nil, nil
	}
	card := &insights.Card{Category: g.category, Title: g.category + " card", Timestamp: now}
	if g.unstamped {
		card.Timestamp = time.Time{}
	}
	return card, nil
}

type fakeStore struct {
	last     map[string]time.Time
	written  []string
	writeErr error
	lastErr  error
}

func newFakeStore() *fakeStore { return &fakeStore{last: map[string]time.Time{}} }

func (f *fakeStore) LastInsightAt(_ context.Context, _ int64, category string) (time.Time, error) {
	if f.lastErr != nil {
		return time.Time{}, f.lastErr
	}
	return f.last[category], nil
}

func (f *fakeStore) InsertInsight(_ context.Context, _ int64, category, _, _ string, at time.Time) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.written = append(f.written, category)
	f.last[category] = at
	return nil
}

func (f *fakeStore) WakeMinutes(context.Context, int64, time.Time, time.Time) ([]int, error) {
	return nil, nil
}
func (f *fakeStore) SleepMinutes(context.Context, int64, time.Time, time.Time) ([]int, error) {
	return nil, nil
}
func (f *fakeStore) AgeYears(context.Context, int64, time.Time) (int, error) { return 0, nil }

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// THE RULE. Three generators, all due, all with something to say. Exactly one
// card is written.
//
// Without the rule this is the shape of the bug: a fresh account has no stored
// cards, so every generator is due at once and the first pass after a deploy
// writes the entire set, all stamped the same minute.
func TestOneCardPerAccountPerPass(t *testing.T) {
	st := newFakeStore()
	gens := []insights.Generator{
		alwaysGen{category: "WAKE_VARIANCE"},
		alwaysGen{category: "SLEEP_DURATION"},
		alwaysGen{category: "HUMIDITY"},
	}

	insightPass(context.Background(), st, gens, 1, time.Now(), quietLog())

	if len(st.written) != 1 {
		t.Fatalf("wrote %v, want exactly one card", st.written)
	}
	if st.written[0] != "WAKE_VARIANCE" {
		t.Errorf("wrote %q first, want the first generator listed", st.written[0])
	}
}

// The runner-up gets the next pass, not a week later.
//
// This is the half of the rule that makes it safe. If a fired generator's own
// cadence also blocked the ones behind it, adding a second generator would
// halve the feed instead of enriching it.
func TestRunnerUpFiresOnTheNextPass(t *testing.T) {
	st := newFakeStore()
	gens := []insights.Generator{
		alwaysGen{category: "WAKE_VARIANCE"},
		alwaysGen{category: "SLEEP_DURATION"},
	}
	now := time.Now()

	insightPass(context.Background(), st, gens, 1, now, quietLog())
	// One InsightInterval later. Far less than either generator's week.
	insightPass(context.Background(), st, gens, 1, now.Add(6*time.Hour), quietLog())

	want := []string{"WAKE_VARIANCE", "SLEEP_DURATION"}
	if len(st.written) != len(want) {
		t.Fatalf("wrote %v, want %v", st.written, want)
	}
	for i := range want {
		if st.written[i] != want[i] {
			t.Errorf("card %d = %q, want %q", i, st.written[i], want[i])
		}
	}

	// A third pass writes nothing: both are now inside their own cadence.
	insightPass(context.Background(), st, gens, 1, now.Add(12*time.Hour), quietLog())
	if len(st.written) != 2 {
		t.Errorf("third pass wrote %v, want nothing new", st.written[2:])
	}
}

// A generator inside its cadence is skipped without consuming the account's
// one card, so the next one down gets the turn.
func TestNotDueYieldsTheTurn(t *testing.T) {
	st := newFakeStore()
	st.last["WAKE_VARIANCE"] = time.Now()

	insightPass(context.Background(), st, []insights.Generator{
		alwaysGen{category: "WAKE_VARIANCE"},
		alwaysGen{category: "SLEEP_DURATION"},
	}, 1, time.Now(), quietLog())

	if len(st.written) != 1 || st.written[0] != "SLEEP_DURATION" {
		t.Errorf("wrote %v, want just SLEEP_DURATION", st.written)
	}
}

// A generator with nothing to say is the normal case and must not consume the
// turn either, or one quiet generator at the top of the list silences the feed.
func TestSilentGeneratorYieldsTheTurn(t *testing.T) {
	st := newFakeStore()

	insightPass(context.Background(), st, []insights.Generator{
		alwaysGen{category: "WAKE_VARIANCE", silent: true},
		alwaysGen{category: "SLEEP_DURATION"},
	}, 1, time.Now(), quietLog())

	if len(st.written) != 1 || st.written[0] != "SLEEP_DURATION" {
		t.Errorf("wrote %v, want just SLEEP_DURATION", st.written)
	}
}

// A generator that errors is logged and skipped, and the pass carries on.
func TestFailedGeneratorYieldsTheTurn(t *testing.T) {
	st := newFakeStore()

	insightPass(context.Background(), st, []insights.Generator{
		alwaysGen{category: "WAKE_VARIANCE", fail: true},
		alwaysGen{category: "SLEEP_DURATION"},
	}, 1, time.Now(), quietLog())

	if len(st.written) != 1 || st.written[0] != "SLEEP_DURATION" {
		t.Errorf("wrote %v, want just SLEEP_DURATION", st.written)
	}
}

// A failed WRITE is not the account's one card. This is the case the `continue`
// in the error branch exists for: turning it into a `break` would take the feed
// silent for a whole interval on a transient database error.
func TestFailedWriteDoesNotConsumeTheTurn(t *testing.T) {
	st := newFakeStore()
	st.writeErr = errors.New("database is on fire")

	gens := []insights.Generator{
		alwaysGen{category: "WAKE_VARIANCE"},
		alwaysGen{category: "SLEEP_DURATION"},
	}
	insightPass(context.Background(), st, gens, 1, time.Now(), quietLog())

	if len(st.written) != 0 {
		t.Fatalf("wrote %v despite the write failing", st.written)
	}

	// Both were attempted, so when the database recovers the next pass works.
	st.writeErr = nil
	insightPass(context.Background(), st, gens, 1, time.Now(), quietLog())
	if len(st.written) != 1 {
		t.Errorf("after recovery wrote %v, want exactly one", st.written)
	}
}

// A generator that forgets to stamp its card must not become due again on the
// very next pass.
//
// Due-ness is `last.IsZero()` against max(timestamp), so an unstamped card
// stores as the zero time and reads back as "never fired". The generator would
// then fire on every pass forever. Found by writing a fake generator that made
// exactly this mistake, which is a fair sign a real one eventually would.
func TestUnstampedCardIsStampedByThePass(t *testing.T) {
	st := newFakeStore()
	gens := []insights.Generator{alwaysGen{category: "WAKE_VARIANCE", unstamped: true}}
	now := time.Now()

	insightPass(context.Background(), st, gens, 1, now, quietLog())
	insightPass(context.Background(), st, gens, 1, now.Add(6*time.Hour), quietLog())

	if len(st.written) != 1 {
		t.Fatalf("wrote %v, want one card; an unstamped card looks like it never fired", st.written)
	}
	if st.last["WAKE_VARIANCE"].IsZero() {
		t.Error("card stored with the zero timestamp")
	}
}

// A LastInsightAt failure must not be read as "never fired", which would make
// every generator look due and write a card on every pass.
func TestLastInsightErrorSkipsRatherThanFires(t *testing.T) {
	st := newFakeStore()
	st.lastErr = errors.New("query failed")

	insightPass(context.Background(), st, []insights.Generator{
		alwaysGen{category: "WAKE_VARIANCE"},
		alwaysGen{category: "SLEEP_DURATION"},
	}, 1, time.Now(), quietLog())

	if len(st.written) != 0 {
		t.Errorf("wrote %v, want nothing when due-ness is unknown", st.written)
	}
}

// The real registry, through the real rule. Guards against a generator being
// added to All() in a way that cannot fire.
func TestRealGeneratorsRespectTheRule(t *testing.T) {
	if len(insights.All()) < 2 {
		t.Skip("the rule only bites with more than one generator")
	}
	st := newFakeStore()
	// The fake store returns no nights, so every real generator declines and
	// nothing is written. That is the correct outcome for an empty account and
	// proves no generator writes without data.
	insightPass(context.Background(), st, insights.All(), 1, time.Now(), quietLog())
	if len(st.written) != 0 {
		t.Errorf("wrote %v from an account with no nights", st.written)
	}
}

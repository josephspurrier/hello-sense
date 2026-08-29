// Package worker runs orb's periodic jobs.
//
// It replaces seven JVM containers (sense-save, pill-save, sense-last-seen,
// smart-alarm, push, insights-generator, aggstats-generator) with goroutines on
// tickers in the same process as the edge. Those containers were never doing
// enough work to justify themselves: the ingest ones are now inline in the edge
// handlers, and what remains is genuinely periodic.
//
// Every job follows the same rules:
//   - it must be safe to run twice, because a restart re-runs whatever was in
//     flight;
//   - it logs what it did rather than only what failed, so a job that silently
//     stops doing anything is visible (agg_stats_worker_enabled sat switched
//     off for weeks precisely because it failed silently);
//   - a failure in one job never stops the others.
package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/josephspurrier/hello-orb/orb/internal/insights"
	"github.com/josephspurrier/hello-orb/orb/internal/push"
	"github.com/josephspurrier/hello-orb/orb/internal/scoring"
	"github.com/josephspurrier/hello-orb/orb/internal/store"
	"github.com/josephspurrier/hello-orb/orb/internal/timeline"
)

// Config controls the job schedule. Defaults suit one household: nothing here
// needs to be fast, and running less often makes the logs readable.
type Config struct {
	TimelineInterval time.Duration
	PruneInterval    time.Duration
	// InsightInterval is how often generators are offered a turn. Cheap, and
	// each generator has its own much longer cadence, so this only bounds how
	// soon a due card appears rather than how often one is written.
	InsightInterval time.Duration
	// MaxNightsPerRun bounds a single pass so a backlog cannot monopolise the
	// algorithm service or produce a thousand-line log burst.
	MaxNightsPerRun int
	// NotifyInterval is how often due push notifications are looked for. The
	// work is one indexed query per kind when there is nothing to send, so this
	// can be frequent without cost.
	NotifyInterval time.Duration
}

func (c *Config) setDefaults() {
	if c.TimelineInterval == 0 {
		c.TimelineInterval = 15 * time.Minute
	}
	if c.PruneInterval == 0 {
		c.PruneInterval = 24 * time.Hour
	}
	if c.InsightInterval == 0 {
		c.InsightInterval = 6 * time.Hour
	}
	if c.MaxNightsPerRun == 0 {
		c.MaxNightsPerRun = 10
	}
	if c.NotifyInterval == 0 {
		c.NotifyInterval = 15 * time.Minute
	}
}

type Worker struct {
	store *store.Store
	// scorer is shared with the timeline write endpoints rather than owned
	// here, so a night scored on the timer and a night scored by a correction
	// go through exactly the same code.
	scorer *scoring.Scorer
	log    *slog.Logger
	cfg    Config
	// push is nil when no signing key is configured, which is a normal state
	// rather than a failure: the notification job simply does nothing.
	push *push.Client
}

func New(s *store.Store, algo timeline.Algorithm, log *slog.Logger, cfg Config) *Worker {
	cfg.setDefaults()
	return &Worker{store: s, scorer: scoring.New(s, algo, log), log: log, cfg: cfg}
}

// WithPush enables push notifications. Separate from New so that a missing or
// unreadable key is a startup decision the caller makes, not something this
// package guesses at.
func (w *Worker) WithPush(c *push.Client) *Worker {
	w.push = c
	return w
}

// Run blocks until ctx is cancelled, running each job on its own ticker.
func (w *Worker) Run(ctx context.Context) {
	w.log.Info("worker starting",
		"timeline_every", w.cfg.TimelineInterval,
		"prune_every", w.cfg.PruneInterval,
		"insights_every", w.cfg.InsightInterval,
		"algorithm", w.scorer.Available())

	go w.loop(ctx, "timeline", w.cfg.TimelineInterval, w.runTimelines)
	go w.loop(ctx, "prune", w.cfg.PruneInterval, w.runPrune)
	go w.loop(ctx, "insights", w.cfg.InsightInterval, w.runInsights)
	go w.loop(ctx, "notifications", w.cfg.NotifyInterval, w.runNotifications)

	<-ctx.Done()
	w.log.Info("worker stopping")
}

// loop runs fn on an interval. It runs once immediately: after a restart the
// most likely state is that something is overdue, and waiting a full interval
// to find out is the wrong default.
func (w *Worker) loop(ctx context.Context, name string, every time.Duration, fn func(context.Context) error) {
	run := func() {
		start := time.Now()
		if err := fn(ctx); err != nil {
			// Logged, never fatal. One job failing must not take down ingest,
			// which shares this process.
			w.log.Error("job failed", "job", name, "err", err, "took", time.Since(start))
			return
		}
		w.log.Debug("job done", "job", name, "took", time.Since(start))
	}

	run()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

// runTimelines scores any night that has no timeline, or whose feedback is
// newer than the stored one.
func (w *Worker) runTimelines(ctx context.Context) error {
	if !w.scorer.Available() {
		// Running without an algorithm is a valid configuration while the Java
		// service is not yet up: ingest still works and nights queue up. Say so
		// once per pass rather than pretending the job ran.
		w.log.Warn("no algorithm configured; timelines not being computed")
		return nil
	}

	todo, err := w.store.NightsNeedingTimeline(ctx, w.cfg.MaxNightsPerRun)
	if err != nil {
		return err
	}
	if len(todo) == 0 {
		return nil
	}
	w.log.Info("scoring nights", "count", len(todo))

	for _, n := range todo {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := w.scorer.ScoreNight(ctx, n.AccountID, n.Date); err != nil {
			// Carry on to the next night. One unscoreable night, typically a
			// gap in the data, must not block every later one.
			w.log.Error("scoring night failed",
				"account", n.AccountID, "date", n.Date.Format(time.DateOnly), "err", err)
		}
	}
	return nil
}

// runPrune deletes data past its useful life.
//
// The old stack had PruneSessions, PruneWebhookEvents and similar written and
// never scheduled, so nothing was ever pruned. Wiring the job up front means it
// cannot be forgotten.
func (w *Worker) runPrune(ctx context.Context) error {
	n, err := w.store.PruneDeliveredMessages(ctx, 7*24*time.Hour)
	if err != nil {
		return err
	}
	if n > 0 {
		w.log.Info("pruned delivered device messages", "rows", n)
	}
	return nil
}

// runInsights generates the cards on the app's insights feed.
//
// AT MOST ONE CARD PER ACCOUNT PER PASS. Generators are offered a turn in the
// order insights.All() lists them and the first one to produce a card ends the
// pass for that account. The reference picks a single category per run through
// a weekly slot, a high-priority list, a random slot and a one-time slot; the
// slots are its business, but the one-card rule is the part that matters and
// this is it.
//
// The rule was invisible while there was one generator and would have gone
// wrong the moment there were two: every generator is due on a fresh account,
// so the first pass after a deploy would have written the whole set at once,
// all stamped the same minute. A feed that arrives in a clump is one people
// stop reading.
//
// Nothing starves. Due-ness comes from what is stored, so a generator that
// fires is not due again for its own interval and the next pass falls through
// to the following one. The runner-up waits one InsightInterval, not a week.
//
// A generator that returns nothing is the normal case and is not logged as a
// problem: most generators have nothing to say most weeks.
func (w *Worker) runInsights(ctx context.Context) error {
	accounts, err := w.store.AccountsWithTimelines(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()

	for _, accountID := range accounts {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		insightPass(ctx, w.store, insights.All(), accountID, now, w.log)
	}
	return nil
}

// insightStore is the slice of the store one insights pass touches.
//
// Narrow, and it exists so the one-card-per-pass rule above can be tested
// without a database. That rule is a single `break` in a loop with three
// `continue`s in it, which is exactly the kind of thing that comes back if
// somebody rearranges the error handling, and the symptom would be a clump of
// cards in the feed rather than a failing anything.
type insightStore interface {
	insights.Data
	LastInsightAt(ctx context.Context, accountID int64, category string) (time.Time, error)
	InsertInsight(ctx context.Context, accountID int64,
		category, title, message string, at time.Time) error
}

// insightPass offers each generator a turn and stops at the first stored card.
//
// Errors are logged and skipped rather than returned: one generator failing is
// not a reason to deny the account every other generator's turn, and the pass
// runs again in an interval regardless.
func insightPass(ctx context.Context, st insightStore, gens []insights.Generator,
	accountID int64, now time.Time, log *slog.Logger) {

	for _, g := range gens {
		// Due-ness is decided from what is stored, not from a timer in
		// memory, so a restart cannot produce a second card the same day.
		last, err := st.LastInsightAt(ctx, accountID, g.Category())
		if err != nil {
			log.Error("insight last-at failed",
				"account", accountID, "category", g.Category(), "err", err)
			continue
		}
		if !last.IsZero() && now.Sub(last) < g.Every() {
			continue
		}

		card, err := g.Generate(ctx, st, accountID, now)
		if err != nil {
			log.Error("insight generation failed",
				"account", accountID, "category", g.Category(), "err", err)
			continue
		}
		if card == nil {
			continue
		}
		// A card with no timestamp of its own is stamped with the pass's.
		//
		// Not a formality. Due-ness is `last.IsZero()`, and LastInsightAt reads
		// max(timestamp), so a card stored with the zero time is indistinguishable
		// from a category that has never fired. The generator would then be due on
		// every single pass and quietly fill the feed. Every generator today sets
		// this, and the field is a copy of an argument they were already handed,
		// so the one place it can be got wrong is a new generator that forgets.
		if card.Timestamp.IsZero() {
			card.Timestamp = now
		}
		if err := st.InsertInsight(ctx, accountID,
			card.Category, card.Title, card.Message, card.Timestamp); err != nil {
			// A failed write is NOT this account's one card. Let the next
			// generator try, or the feed goes silent for a whole interval
			// because of a transient error.
			log.Error("insight store failed",
				"account", accountID, "category", g.Category(), "err", err)
			continue
		}
		log.Info("generated insight",
			"account", accountID, "category", card.Category, "title", card.Title)
		return
	}
}

package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/josephspurrier/hello-orb/orb/internal/push"
)

// Notification kinds. These are the dedupe namespaces as well as the log
// labels, so they are constants rather than literals scattered about: a typo in
// one place would silently create a second namespace that never dedupes against
// the first.
const (
	kindSleepScore  = "sleep_score"
	kindPillBattery = "pill_battery"
)

// settingForKind maps a notification kind onto the toggle in the app's
// Notifications screen that governs it. Sleep score has its own switch; the
// battery warning falls under System Alerts, there being no pill-specific
// toggle on that screen.
var settingForKind = map[string]string{
	kindSleepScore:  "SLEEP_SCORE",
	kindPillBattery: "SYSTEM",
}

// scoreWindow bounds how old a night may be and still be announced.
//
// Without it, the first run against a database of history would send one
// notification per night ever recorded. A day is generous: the timeline job runs
// every fifteen minutes, so a score reaches this within the hour under any
// normal circumstance, and anything older is not news.
const scoreWindow = 24 * time.Hour

// pillBatteryThreshold matches the reference's battery_notification_threshold.
// Unlike the reference, orb wants two consecutive heartbeats under it before
// it says anything; see Store.PillsBelowBattery for why.
const pillBatteryThreshold = 10

// runNotifications sends the notifications that are due.
//
// Nothing here retries within a run. A send that fails releases its claim and is
// picked up by the next tick, which is the difference between a transient APNS
// blip costing fifteen minutes and it costing the notification entirely.
func (w *Worker) runNotifications(ctx context.Context) error {
	if w.push == nil {
		// Not configured is a normal state, not an error: push needs a signing
		// key that a development machine may not have.
		return nil
	}

	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	note(w.notifySleepScores(ctx))
	note(w.notifyPillBattery(ctx))
	return firstErr
}

func (w *Worker) notifySleepScores(ctx context.Context) error {
	nights, err := w.store.RecentScoredNights(ctx, scoreWindow)
	if err != nil {
		return err
	}
	for _, n := range nights {
		key := n.Date.Format("2006-01-02")
		body := fmt.Sprintf("You slept %d last night. Tap to see your timeline.", n.Score)
		w.deliver(ctx, n.AccountID, kindSleepScore, key, push.Alert{
			Title: "Your sleep score",
			Body:  body,
		})
	}
	return nil
}

func (w *Worker) notifyPillBattery(ctx context.Context) error {
	pills, err := w.store.PillsBelowBattery(ctx, pillBatteryThreshold)
	if err != nil {
		return err
	}
	// Bucketed by ISO week, matching the reference's WEEKLY periodicity. A flat
	// battery stays flat, so a daily reminder would be a daily annoyance.
	year, week := time.Now().ISOWeek()
	key := fmt.Sprintf("%d-W%02d", year, week)
	for _, p := range pills {
		body := fmt.Sprintf("Your Sleep Pill's battery is at %d%%. Time to replace it.", p.Battery)
		w.deliver(ctx, p.AccountID, kindPillBattery, key, push.Alert{
			Title: "Sleep Pill battery low",
			Body:  body,
		})
	}
	return nil
}

// deliver claims the notification, sends it to every device registered to the
// account, and releases the claim if nothing could be delivered.
//
// Errors are logged rather than returned, so one account's dead token cannot
// stop another account's notification.
func (w *Worker) deliver(ctx context.Context, accountID int64, kind, key string, alert push.Alert) {
	// The app's Notifications screen toggle for this kind. Checked BEFORE the
	// claim on purpose: a notification suppressed while the toggle is off
	// stays claimable, so flipping it back on within the window still
	// delivers rather than finding the claim already burned.
	if setting, gated := settingForKind[kind]; gated {
		on, err := w.store.NotificationEnabled(ctx, accountID, setting)
		if err != nil {
			w.log.Error("notification setting", "kind", kind, "account", accountID, "err", err)
			return
		}
		if !on {
			return
		}
	}

	claimed, err := w.store.ClaimPush(ctx, accountID, kind, key)
	if err != nil {
		w.log.Error("push claim failed", "kind", kind, "account", accountID, "err", err)
		return
	}
	if !claimed {
		return // already sent, the normal case on most ticks
	}

	tokens, err := w.store.PushTokensFor(ctx, accountID)
	if err != nil {
		w.log.Error("push tokens", "account", accountID, "err", err)
		w.release(ctx, accountID, kind, key)
		return
	}
	if len(tokens) == 0 {
		// No phone registered. Release rather than hold the claim, so that
		// registering a device later still gets the next notification instead
		// of finding this one already marked sent.
		w.release(ctx, accountID, kind, key)
		return
	}

	var sent int
	for _, t := range tokens {
		err := w.push.Send(ctx, t.Token, alert)
		switch {
		case err == nil:
			sent++
			if err := w.store.MarkPushTokenSent(ctx, t.Token); err != nil {
				w.log.Warn("mark push sent", "err", err)
			}
		case errors.Is(err, push.ErrUnregistered):
			// The app is gone or the token was replaced. Keeping it would mean
			// failing forever on a device that no longer exists.
			w.log.Info("forgetting dead push token", "account", accountID)
			if err := w.store.ForgetPushToken(ctx, t.Token); err != nil {
				w.log.Warn("forget push token", "err", err)
			}
		default:
			w.log.Error("push send failed", "kind", kind, "account", accountID, "err", err)
		}
	}

	if sent == 0 {
		w.release(ctx, accountID, kind, key)
		return
	}
	w.log.Info("push sent", "kind", kind, "account", accountID, "key", key, "devices", sent)
}

func (w *Worker) release(ctx context.Context, accountID int64, kind, key string) {
	if err := w.store.ReleasePush(ctx, accountID, kind, key); err != nil {
		w.log.Error("push release failed", "kind", kind, "account", accountID, "err", err)
	}
}

package edge

import (
	"testing"
	"time"
)

// The clock guard is asymmetric, and the asymmetry is the point.
//
// It exists to catch a device whose clock has not synced: the Orb starts around
// 1956 after a reboot, per knowledgebase/DEVICE-PROTOCOL.md. The original bound
// was a symmetric two hours, which catches that case and also catches something
// it should not: a healthy device flushing a backlog.
//
// On 2026-08-16 LocalStack's Kinesis died at 23:40 local. The device got 500s
// for nine hours, buffered, and re-sent samples up to two and a half hours old
// when service returned. The symmetric window logged 492 "unsynced clock"
// warnings across the night. Little was lost that time because most samples had
// already been stored on an earlier attempt, but a longer outage would have
// discarded a whole night of good data.
func TestClockGuardKeepsBacklogAndRejectsBrokenClocks(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

	for _, c := range []struct {
		name string
		ts   time.Time
		want bool
	}{
		{"right now", now, true},
		{"a minute old", now.Add(-time.Minute), true},

		// The case that prompted the fix.
		{"the backlog that was wrongly dropped", now.Add(-150 * time.Minute), true},
		{"a full night of buffered data", now.Add(-12 * time.Hour), true},
		{"a bug spanning two nights", now.Add(-48 * time.Hour), true},
		{"nearly a week offline", now.Add(-6 * 24 * time.Hour), true},

		// Still rejected: a clock that is simply wrong.
		{"a device that thinks it is 1956", time.Date(1956, 1, 1, 0, 0, 0, 0, time.UTC), false},
		{"the unix epoch", time.Unix(0, 0).UTC(), false},
		{"further back than any plausible outage", now.Add(-30 * 24 * time.Hour), false},

		// Ahead of the server cannot be a backlog, so it stays tight.
		{"slightly ahead, ordinary skew", now.Add(30 * time.Minute), true},
		{"hours into the future", now.Add(5 * time.Hour), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := plausibleSampleTime(c.ts, now); got != c.want {
				t.Errorf("plausibleSampleTime(%v) = %v, want %v (delta %v)",
					c.ts, got, c.want, c.ts.Sub(now))
			}
		})
	}
}

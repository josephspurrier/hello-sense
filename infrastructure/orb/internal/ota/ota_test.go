package ota

import (
	"bytes"
	"testing"
	"time"
)

func goodSHA() []byte { return bytes.Repeat([]byte{0xAB}, 20) }

func armed() *Update {
	from := int32(4513)
	return &Update{
		DeviceID: "TESTSENSE", FromVersion: &from, ToVersion: 4514,
		Host: "example.invalid", URL: "/kitsune.bin?sig=x",
		SHA1: goodSHA(), FileSize: 146864, Armed: true,
	}
}

// 03:00 is inside the default 02:00-05:00 window.
func inWindow() time.Time { return time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC) }

// The only path that offers an update, so that every refusal below is measured
// against a case that definitely would have said yes.
func TestOffersWhenEverythingIsRight(t *testing.T) {
	d := Decide(armed(), 4513, time.Hour, inWindow(), DefaultPolicy)
	if !d.Offer {
		t.Fatalf("refused a valid update: %s", d.Reason)
	}
}

// Every gate refuses on its own. Each case starts from the offer-able update
// above and breaks exactly one thing, so a gate that stops working is visible
// rather than masked by another.
func TestEveryGateRefuses(t *testing.T) {
	for _, c := range []struct {
		name    string
		mutate  func(*Update)
		version int32
		uptime  time.Duration
		now     time.Time
	}{
		{"not armed", func(u *Update) { u.Armed = false }, 4513, time.Hour, inWindow()},
		{"already completed", func(u *Update) { u.Completed = true }, 4513, time.Hour, inWindow()},
		{"wrong current version", nil, 4499, time.Hour, inWindow()},
		{"already on the target", func(u *Update) { u.FromVersion = nil }, 4514, time.Hour, inWindow()},
		{"digest missing", func(u *Update) { u.SHA1 = nil }, 4513, time.Hour, inWindow()},
		{"digest truncated", func(u *Update) { u.SHA1 = []byte{1, 2, 3} }, 4513, time.Hour, inWindow()},
		{"no file size", func(u *Update) { u.FileSize = 0 }, 4513, time.Hour, inWindow()},
		{"no host", func(u *Update) { u.Host = "" }, 4513, time.Hour, inWindow()},
		{"no url", func(u *Update) { u.URL = "" }, 4513, time.Hour, inWindow()},
		{"uptime not reported", nil, 4513, -1, inWindow()},
		{"only just booted", nil, 4513, time.Minute, inWindow()},
		{"outside the window", nil, 4513, time.Hour, time.Date(2026, 8, 17, 21, 0, 0, 0, time.UTC)},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := armed()
			if c.mutate != nil {
				c.mutate(u)
			}
			if d := Decide(u, c.version, c.uptime, c.now, DefaultPolicy); d.Offer {
				t.Errorf("offered an update it should have refused (%s)", d.Reason)
			}
		})
	}
}

// No prepared update is the state every device is in by default, and it must
// never produce an offer.
func TestNoUpdateNeverOffers(t *testing.T) {
	if d := Decide(nil, 4513, time.Hour, inWindow(), DefaultPolicy); d.Offer {
		t.Errorf("offered an update with nothing prepared: %s", d.Reason)
	}
}

// A successful flash ends the offer by itself: the device comes back reporting
// the new version, stops matching from_version, and is refused. Without this,
// an update would be re-offered forever.
func TestSuccessfulUpdateStopsBeingOffered(t *testing.T) {
	u := armed()
	if d := Decide(u, 4513, time.Hour, inWindow(), DefaultPolicy); !d.Offer {
		t.Fatalf("should offer before the flash: %s", d.Reason)
	}
	if d := Decide(u, 4514, time.Hour, inWindow(), DefaultPolicy); d.Offer {
		t.Error("still offering after the device reported the new version")
	}
}

// The window may wrap past midnight, which is where it wants to be.
func TestWindowWrapsMidnight(t *testing.T) {
	w := Window{StartHour: 23, EndHour: 4}
	for _, c := range []struct {
		hour int
		want bool
	}{{23, true}, {0, true}, {3, true}, {4, false}, {12, false}, {22, false}} {
		got := w.contains(time.Date(2026, 8, 17, c.hour, 30, 0, 0, time.UTC))
		if got != c.want {
			t.Errorf("hour %d: contains = %v, want %v", c.hour, got, c.want)
		}
	}
}

// The uptime gate is configurable so a test loop does not wait 20 minutes
// between attempts. This pins that it is actually honoured, because a gate that
// silently ignores its setting is worse than one that cannot be changed: you
// wait anyway and conclude something else is wrong.
func TestMinUptimeIsHonoured(t *testing.T) {
	short := Policy{Window: DefaultWindow, MinUptime: 5 * time.Minute}

	// Six minutes: refused by the default, allowed by the short policy.
	if d := Decide(armed(), 4513, 6*time.Minute, inWindow(), DefaultPolicy); d.Offer {
		t.Fatal("default policy offered to a device up only 6 minutes")
	}
	if d := Decide(armed(), 4513, 6*time.Minute, inWindow(), short); !d.Offer {
		t.Fatalf("short policy refused a device up 6 minutes: %s", d.Reason)
	}
	// Still refused below the shortened gate.
	if d := Decide(armed(), 4513, 4*time.Minute, inWindow(), short); d.Offer {
		t.Fatal("short policy offered below its own minimum")
	}
}

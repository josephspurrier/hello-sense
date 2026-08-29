// Package ota decides whether a Sense should be offered a firmware update.
//
// This is the most dangerous code in orb. Everything else here can serve a
// wrong number; this writes to the flash of a device that cannot be replaced,
// and a bad image is not a bug report, it is a brick.
//
// The design follows from that. The reference decides OTA from feature flags
// and device groups, which suits a fleet: flip a flag, a cohort updates. That
// is exactly wrong for one irreplaceable unit, because a flag flipped for a
// group is an update nobody deliberately aimed at this device. Here an update
// is a row that names the device, and offering it requires arming it.
//
// Every gate below can only ever REFUSE. There is no path that turns an
// unarmed, unmatched or malformed update into an offer.
package ota

import "time"

// Update is a prepared firmware image for one device.
type Update struct {
	DeviceID    string
	FromVersion *int32 // nil means any version, which is not the normal case
	ToVersion   int32
	Host        string
	URL         string
	SHA1        []byte
	FileSize    int32
	Armed       bool
	Completed   bool

	CopyToSerialFlash         bool
	ResetApplicationProcessor bool
	ResetNetworkProcessor     bool
	SerialFlashFilename       string
	SerialFlashPath           string
	SDCardFilename            string
	SDCardPath                string
}

// Window is when updates may be offered, as hours in the device's local time.
//
// The reference has the same idea and for the same reason: an update that
// starts while somebody is asleep, on a device that is also their alarm clock,
// is a bad night at best. Defaults are deliberately the small hours, and a
// device that reboots mid-flash has all night to recover before it is needed.
type Window struct {
	StartHour int
	EndHour   int
}

// DefaultWindow is 02:00 to 05:00 local.
var DefaultWindow = Window{StartHour: 2, EndHour: 5}

// MinUptime is how long a device must have been running before it is offered an
// update.
//
// A device that just booted may be rebooting repeatedly, and handing an image
// to something in a boot loop is how a recoverable fault becomes a brick. The
// reference calls this deviceUptimeDelay.
const MinUptime = 20 * time.Minute

// sha1Length guards against a truncated or absent digest reaching the device.
const sha1Length = 20

// Decision is why an update was or was not offered. Returned rather than logged
// inside, so the caller can record the reason on a path that otherwise leaves
// no trace.
type Decision struct {
	Offer  bool
	Reason string
}

// Decide reports whether to offer an update.
//
// uptime is the device's reported uptime; a negative value means it did not
// report one, which is treated as unknown and therefore refused. localNow is
// the device's local time, because the window is a statement about the
// sleeper's night rather than about UTC.
func Decide(u *Update, currentVersion int32, uptime time.Duration, localNow time.Time, w Window) Decision {
	if u == nil {
		return Decision{false, "no update prepared"}
	}
	if u.Completed {
		return Decision{false, "update already completed"}
	}
	if !u.Armed {
		// The normal state. A row exists so somebody can inspect it before
		// deciding, and preparing an update is not the same as authorising it.
		return Decision{false, "update not armed"}
	}
	if u.FromVersion != nil && *u.FromVersion != currentVersion {
		// Also the mechanism that ends an update: once the device reports the
		// new version it stops matching, so a successful flash is not re-offered.
		return Decision{false, "device is not on the expected version"}
	}
	if u.ToVersion == currentVersion {
		return Decision{false, "device is already on the target version"}
	}
	if len(u.SHA1) != sha1Length {
		return Decision{false, "digest missing or wrong length"}
	}
	if u.FileSize <= 0 {
		return Decision{false, "file size missing"}
	}
	if u.Host == "" || u.URL == "" {
		return Decision{false, "no location to fetch from"}
	}
	if uptime < 0 {
		return Decision{false, "device did not report uptime"}
	}
	if uptime < MinUptime {
		return Decision{false, "device has not been up long enough"}
	}
	if !w.contains(localNow) {
		return Decision{false, "outside the update window"}
	}
	return Decision{true, "armed, matched and inside the window"}
}

// contains reports whether a local time falls in the window. A window that
// wraps past midnight is supported, because the small hours are exactly where
// this wants to run.
func (w Window) contains(t time.Time) bool {
	h := t.Hour()
	if w.StartHour <= w.EndHour {
		return h >= w.StartHour && h < w.EndHour
	}
	return h >= w.StartHour || h < w.EndHour
}

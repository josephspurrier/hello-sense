package store

import (
	"testing"
	"time"
)

func TestZoneOffsetMSFollowsDST(t *testing.T) {
	const edt = -4 * 3600 * 1000
	cases := []struct {
		zone string
		at   time.Time
		want int32
	}{
		{"America/New_York", time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), edt},
		{"America/New_York", time.Date(2026, 11, 2, 13, 10, 0, 0, time.UTC), -5 * 3600 * 1000},
		{"", time.Date(2026, 11, 2, 13, 10, 0, 0, time.UTC), edt},
		{"Not/AZone", time.Date(2026, 11, 2, 13, 10, 0, 0, time.UTC), edt},
	}
	for _, c := range cases {
		if got := zoneOffsetMS(c.zone, c.at, edt); got != c.want {
			t.Errorf("zoneOffsetMS(%q, %v) = %d, want %d", c.zone, c.at, got, c.want)
		}
	}
}

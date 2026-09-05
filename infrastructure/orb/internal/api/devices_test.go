package api

import "testing"

func TestPillState(t *testing.T) {
	i32 := func(v int32) *int32 { return &v }
	cases := []struct {
		name    string
		battery *int32
		want    string
	}{
		{"no heartbeat yet", nil, "UNKNOWN"},
		{"flat", i32(0), "LOW_BATTERY"},
		{"just under the line", i32(14), "LOW_BATTERY"},
		{"on the line", i32(15), "NORMAL"},
		{"healthy", i32(54), "NORMAL"},
		{"over 100, the pill's >3.0V branch", i32(101), "NORMAL"},
	}
	for _, c := range cases {
		if got := pillState(c.battery); got != c.want {
			t.Errorf("%s: pillState = %q, want %q", c.name, got, c.want)
		}
	}
}

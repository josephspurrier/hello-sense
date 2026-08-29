package scoring

import "testing"

// The guard's own comparison, which got this wrong on its first run.
//
// `new_time` is a SQL TIME and comes back as "06:50:00" while the algorithm's
// event formats as "06:50", so the first version reported every correction as
// unapplied. A false alarm on a check that exists to catch silent failure is
// worse than no check: it teaches you to ignore the warning.
func TestHHMM(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"06:50:00", "06:50"},
		{"08:35:00", "08:35"},
		{"08:35", "08:35"},
		{"23:59:59", "23:59"},
		{"", ""},
		{"bad", "bad"}, // too short to truncate; compares unequal and warns
	} {
		if got := hhmm(c.in); got != c.want {
			t.Errorf("hhmm(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

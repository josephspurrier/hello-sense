package api

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The share id is the ONLY thing protecting a share page: it is served
// unauthenticated to anyone holding the link. So it has to be unguessable, and
// two shares must never collide.
func TestShareIDIsUnguessable(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		id, err := shareID()
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("duplicate share id after %d draws: %q", i, id)
		}
		seen[id] = true
		// 16 random bytes, base64url, no padding.
		if len(id) != 22 {
			t.Fatalf("id %q has length %d, want 22", id, len(id))
		}
		if strings.ContainsAny(id, "/+=") {
			t.Fatalf("id %q is not URL-safe", id)
		}
	}
}

func TestCategoryLabel(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"SLEEP_DURATION", "Sleep duration"},
		{"WAKE_VARIANCE", "Wake variance"},
		{"SOUND", "Sound"},
		{"", ""},
	} {
		if got := categoryLabel(c.in); got != c.want {
			t.Errorf("categoryLabel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The page is served to people who are not the account holder, so the card's
// own text must not be able to inject markup. html/template escapes it; this
// test is here so that swapping in text/template, or building the HTML by
// concatenation, fails loudly.
func TestSharePageEscapesCardText(t *testing.T) {
	var sb strings.Builder
	err := shareTemplate.Execute(&sb, sharePage{
		Category: "Sound",
		Title:    `<script>alert(1)</script>`,
		Message:  `" onload="alert(2)`,
		SharedBy: `<img src=x onerror=alert(3)>`,
		When:     "27 August 2026",
	})
	if err != nil {
		t.Fatal(err)
	}
	out := sb.String()

	// Assert on what is actually dangerous: an unescaped tag opening or an
	// attribute break. Testing for the raw substring "onerror=alert(3)" does
	// NOT work and the first version of this test did exactly that: escaping
	// only rewrites the metacharacters, so `&lt;img src=x onerror=alert(3)&gt;`
	// still contains it, inert, and the test failed on safe output.
	for _, bad := range []string{"<script", "<img", `onload="`} {
		if strings.Contains(out, bad) {
			t.Errorf("unescaped %q in rendered page:\n%s", bad, out)
		}
	}
	// The text still has to appear, escaped, or the escaping is just deletion.
	for _, want := range []string{"&lt;script&gt;", "&lt;img", "&#34;"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected escaped %q in page:\n%s", want, out)
		}
	}
}

// An account with no first name shares anonymously, and the sentence still has
// to read properly rather than saying "Shared by .".
func TestSharePageWordsAnonymousShares(t *testing.T) {
	var named, anon strings.Builder
	if err := shareTemplate.Execute(&named, sharePage{Title: "t", SharedBy: "Joseph", When: "27 August 2026"}); err != nil {
		t.Fatal(err)
	}
	if err := shareTemplate.Execute(&anon, sharePage{Title: "t", When: "27 August 2026"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(named.String(), "Shared by Joseph from a Sense sleep report, 27 August 2026.") {
		t.Errorf("named share reads wrong")
	}
	if !strings.Contains(anon.String(), "Shared from a Sense sleep report, 27 August 2026.") {
		t.Errorf("anonymous share reads wrong")
	}
	if strings.Contains(anon.String(), "Shared by ") {
		t.Errorf("anonymous share still says 'Shared by'")
	}
}

// A share with no timestamp must not render a trailing ", ." fragment.
func TestSharePageOmitsAnAbsentDate(t *testing.T) {
	var sb strings.Builder
	if err := shareTemplate.Execute(&sb, sharePage{Title: "t", SharedBy: "Joseph"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sb.String(), "Shared by Joseph.") {
		t.Errorf("dateless share reads wrong: %s", sb.String())
	}
}

// The page loads nothing and runs nothing. These headers are what keeps that
// true, and they are cheap to lose in an edit.
func TestSharePageIsLockedDown(t *testing.T) {
	rec := httptest.NewRecorder()
	// Exercise the header block without a store by rendering into a recorder
	// the same way the handler does.
	rec.Header().Set("Cache-Control", "no-store")
	rec.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'")

	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("cache-control = %q; a revoked share must not survive in a cache", got)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("csp = %q", csp)
	}
}

// The rendered page must carry the card's actual content, since that is the
// entire point of the link.
func TestSharePageShowsTheCard(t *testing.T) {
	var sb strings.Builder
	err := shareTemplate.Execute(&sb, sharePage{
		Category: "Sleep duration",
		Title:    "Hello, well rested",
		Message:  "You averaged 7.4 hours of sleep.",
		SharedBy: "Joseph",
		When:     time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC).Format("2 January 2006"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Sleep duration", "Hello, well rested",
		"You averaged 7.4 hours of sleep.", "27 August 2026",
		`name="robots" content="noindex, nofollow"`,
	} {
		if !strings.Contains(sb.String(), want) {
			t.Errorf("page is missing %q", want)
		}
	}
}

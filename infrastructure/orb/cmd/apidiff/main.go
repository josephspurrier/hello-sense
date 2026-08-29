// Command apidiff compares orb's app API against the running Java stack.
//
// It sends the same authenticated request to both and diffs the decoded JSON,
// because that is the only check that answers the question the app cares about:
// does it receive the same thing. A Go test proves orb is self-consistent, and
// reading suripu's resource classes tells you which fields exist; neither
// notices a field that is present but means something different. Every defect
// worth having found in this rewrite was found by a diff and not by a test.
//
// The token is read from orb's own oauth_tokens table and never printed. It is
// the same token the app holds, migrated across, so it authenticates against
// both stacks. Passing a credential on a command line or through a log is how
// it ends up somewhere it should not be.
//
// Usage:
//
//	go run ./cmd/apidiff -account 1 GET /v1/account GET /v1/timezone
//
// With no paths it runs the default set. Exit status is non-zero if anything
// differs, so it can gate a change.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The default sweep: the read endpoints the app leans on, most-called first.
// Writes are deliberately absent. A diff tool that mutates state changes the
// thing it is measuring, and running it twice would compare a fresh account
// against an altered one.
// Every implemented read endpoint is listed, so a bare `go run ./cmd/apidiff`
// is the regression sweep rather than a two-endpoint spot check. Adding a
// handler means adding its line here; an endpoint that is not on this list is
// only ever checked by whoever remembers to name it.
//
// /v2/trends is deliberately absent. orb's scoring model has diverged from
// suripu's on three of the five nights this account has, so trends can never
// come back clean and a permanently red line in the sweep is one nobody reads.
// It is checked instead by TestTrendsMatchesReference, which drives orb's
// rendering with the reference's own aggregates against captured golden
// responses. That separates a rendering bug from a scoring difference, which a
// live diff of this endpoint cannot do.
var defaultPaths = []string{
	"GET /v2/sensors",
	"GET /v2/timeline/2026-08-13",
	"GET /v1/account",
	"GET /v2/account/preferences",
	"GET /v2/devices",
	"GET /v1/app/stats/unread",
	"GET /v2/insights",
	"GET /v2/alerts",
	"GET /v2/alarms",
	"GET /v1/timezone",
	"GET /v2/sleep_sounds/status",
	"GET /v2/insights/info/WAKE_VARIANCE",
	"GET /v2/insights/info/SOUND",
}

// Two implemented endpoints are deliberately NOT in the sweep, for the same
// reason /v2/trends is not: they can never come back clean, and a permanently
// red line is one nobody reads.
//
//	GET /v2/alarms/sounds
//	GET /v2/sleep_sounds/combined_state
//
// Both differ only in the audio URL field, which orb omits because the audio is
// gone: the `hello-audio` bucket is empty and a content-hash sweep of all 135
// repositories found none of the tones. suripu's own URLs are unusable anyway,
// signed against the Docker-internal host `localstack:4566` with a fresh expiry
// and signature on every call, so this endpoint could never diff byte-for-byte
// even if the files existed.
//
// The parts that matter, the ids and names, are pinned instead by
// TestAlarmSoundsMatchTheReference and TestSleepSoundsMatchFileInfo, and
// TestNoAudioURLsAreServed makes serving a URL again a deliberate edit. That
// separates "the list is wrong" from "the audio is missing", which a live diff
// of these endpoints cannot do.
//
// POST /v2/sharing/insight is absent because it is a write. See the note on
// defaultPaths above.

func main() {
	var (
		dsn     = flag.String("dsn", envOr("ORB_DSN", "postgres://hello:hello@localhost:5432/orb"), "Postgres DSN")
		javaURL = flag.String("java", "http://localhost:9999", "suripu-app base URL")
		orbURL  = flag.String("orb", "http://localhost:8082", "orb app API base URL")
		account = flag.Int64("account", 1, "account id whose token to use")
		timeout = flag.Duration("timeout", 20*time.Second, "per-request timeout")
		verbose = flag.Bool("v", false, "print both bodies even when they match")
		// Reading suripu's resource classes to learn a response shape has now
		// been wrong twice in ways the classes did not reveal: `id` is an
		// external uuid, `dob` is a string beside millis. Asking the running
		// service is faster and it cannot be misread.
		show = flag.Bool("show", false, "print the Java response for each target and exit, without calling orb")
	)
	flag.Parse()

	targets := flag.Args()
	if len(targets) == 0 {
		targets = defaultPaths
	} else if len(targets)%2 == 0 && isMethod(targets[0]) {
		// "GET /v1/account GET /v1/timezone" arrives as separate args; join
		// them back into "METHOD PATH" pairs.
		joined := make([]string, 0, len(targets)/2)
		for i := 0; i < len(targets); i += 2 {
			joined = append(joined, targets[i]+" "+targets[i+1])
		}
		targets = joined
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		fatal("open store: %v", err)
	}
	defer pool.Close()

	token, err := liveToken(ctx, pool, *account)
	if err != nil {
		fatal("%v", err)
	}

	client := &http.Client{Timeout: *timeout}
	var failed int

	if *show {
		for _, t := range targets {
			method, path, ok := strings.Cut(t, " ")
			if !ok {
				fatal("target %q is not \"METHOD /path\"", t)
			}
			body, code, err := fetch(client, method, *javaURL+path, token)
			if err != nil {
				fmt.Printf("=== %s  ERROR %v\n", t, err)
				continue
			}
			fmt.Printf("=== %s  (%d)\n%s\n", t, code, indent(body))
		}
		return
	}

	for _, t := range targets {
		method, path, ok := strings.Cut(t, " ")
		if !ok {
			fatal("target %q is not \"METHOD /path\"", t)
		}

		javaBody, javaCode, jErr := fetch(client, method, *javaURL+path, token)
		orbBody, orbCode, oErr := fetch(client, method, *orbURL+path, token)

		switch {
		case jErr != nil:
			fmt.Printf("%-34s  JAVA UNREACHABLE  %v\n", t, jErr)
			failed++
			continue
		case oErr != nil:
			fmt.Printf("%-34s  ORB UNREACHABLE   %v\n", t, oErr)
			failed++
			continue
		}

		if javaCode != orbCode {
			fmt.Printf("%-34s  STATUS  java=%d orb=%d\n", t, javaCode, orbCode)
			failed++
			continue
		}

		diffs, drift := splitDrift(diffJSON(javaBody, orbBody))
		for _, d := range drift {
			fmt.Printf("%-34s  drift   %s\n", t, d)
		}
		if len(diffs) == 0 {
			fmt.Printf("%-34s  match (%d)\n", t, javaCode)
			if *verbose {
				fmt.Printf("    %s\n", javaBody)
			}
			continue
		}

		failed++
		fmt.Printf("%-34s  DIFFERS (%d fields)\n", t, len(diffs))
		for _, d := range diffs {
			fmt.Printf("    %s\n", d)
		}
	}

	if failed > 0 {
		fmt.Printf("\n%d of %d differ\n", failed, len(targets))
		os.Exit(1)
	}
	fmt.Printf("\nall %d match\n", len(targets))
}

// liveToken picks an unexpired token for the account.
//
// It returns the value only into memory. Nothing prints it, and the error on
// the empty case says how to fix it without quoting anything sensitive.
func liveToken(ctx context.Context, pool *pgxpool.Pool, accountID int64) (string, error) {
	var uuid string
	var appID int64
	err := pool.QueryRow(ctx, `
		SELECT access_token, app_id FROM oauth_tokens
		WHERE account_id = $1 AND expires_at > now()
		ORDER BY expires_at DESC LIMIT 1`, accountID).Scan(&uuid, &appID)
	if err != nil {
		return "", fmt.Errorf("no unexpired token for account %d: open the iOS app to mint one (%w)", accountID, err)
	}
	// The column holds a UUID; the wire format is "{appId}.{uuid without
	// dashes}". Sending the column value authenticates against an
	// implementation that made the same mistake and against nothing else.
	return fmt.Sprintf("%d.%s", appID, strings.ReplaceAll(uuid, "-", "")), nil
}

func fetch(c *http.Client, method, url, token string) ([]byte, int, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	return body, resp.StatusCode, err
}

// diffJSON compares two JSON documents field by field and returns one line per
// difference.
//
// Structural comparison rather than byte comparison, because key order and
// whitespace are not part of the contract and treating them as differences
// would bury the ones that matter.
func diffJSON(a, b []byte) []string {
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return []string{fmt.Sprintf("java body is not JSON: %v", err)}
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return []string{fmt.Sprintf("orb body is not JSON: %v", err)}
	}
	var out []string
	walk("", av, bv, &out)
	sort.Strings(out)
	return out
}

func walk(path string, a, b any, out *[]string) {
	switch at := a.(type) {
	case map[string]any:
		bt, ok := b.(map[string]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: java=object orb=%T", path, b))
			return
		}
		seen := map[string]bool{}
		for k, av := range at {
			seen[k] = true
			bv, present := bt[k]
			if !present {
				*out = append(*out, fmt.Sprintf("%s: MISSING from orb (java=%v)", join(path, k), av))
				continue
			}
			walk(join(path, k), av, bv, out)
		}
		for k, bv := range bt {
			if !seen[k] {
				*out = append(*out, fmt.Sprintf("%s: EXTRA in orb (orb=%v)", join(path, k), bv))
			}
		}
	case []any:
		bt, ok := b.([]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: java=array orb=%T", path, b))
			return
		}
		if len(at) != len(bt) {
			*out = append(*out, fmt.Sprintf("%s: length java=%d orb=%d", path, len(at), len(bt)))
			return
		}
		for i := range at {
			walk(fmt.Sprintf("%s[%d]", path, i), at[i], bt[i], out)
		}
	default:
		if !reflect.DeepEqual(a, b) {
			*out = append(*out, fmt.Sprintf("%s: java=%v orb=%v", path, a, b))
		}
	}
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// driftFields are the ones that legitimately move between two calls a few
// milliseconds apart, because they report "when did we last hear from the
// device" and the two stacks learn that from different sources at different
// rates.
//
// The list is deliberately tiny and matched on the full field name rather than
// a substring. Suppressing a difference is how a real one hides, so anything
// added here needs a reason that is about the value being live, not about the
// diff being inconvenient. Drift is still printed, it just does not fail the
// run.
var driftFields = map[string]bool{
	"last_updated": true,
}

func splitDrift(all []string) (diffs, drift []string) {
	for _, d := range all {
		// Lines are "path: java=... orb=...". Take the last path segment.
		path, _, _ := strings.Cut(d, ":")
		leaf := path
		if i := strings.LastIndexByte(path, '.'); i >= 0 {
			leaf = path[i+1:]
		}
		if driftFields[leaf] {
			drift = append(drift, d)
			continue
		}
		diffs = append(diffs, d)
	}
	return diffs, drift
}

// indent re-renders a JSON body readably, or returns it untouched if it is not
// JSON, which is itself worth seeing.
func indent(body []byte) string {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return string(body)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(body)
	}
	return string(out)
}

func isMethod(s string) bool {
	switch strings.ToUpper(s) {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
		return true
	}
	return false
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "apidiff: "+format+"\n", args...)
	os.Exit(2)
}

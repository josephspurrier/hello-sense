// Command compare diffs what orb ingested against what the Java stack stored
// for the same minutes, field by field.
//
// Both systems are fed the identical device upload: sense_server.py forwards it
// to suripu-service and mirrors a copy to orb. They write to separate stores
// (DynamoDB vs Postgres) and neither can see the other, so any disagreement is
// a real difference in interpretation rather than a shared bug.
//
// This is the evidence a cutover needs. A green test suite proved only
// self-consistency; the signature padding bug passed every unit test and would
// have rejected every real upload.
//
// Usage:
//
//	./scripts/dump-dynamo.sh                       # fresh snapshot
//	go run ./cmd/compare -since '2026-08-13T18:40:00Z'
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type attr struct {
	S *string `json:"S"`
	N *string `json:"N"`
}

type item map[string]attr

func (i item) str(k string) string {
	if a, ok := i[k]; ok && a.S != nil {
		return *a.S
	}
	return ""
}

func (i item) num(k string) (int64, bool) {
	a, ok := i[k]
	if !ok || a.N == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(*a.N, 64)
	if err != nil {
		return 0, false
	}
	return int64(f), true
}

// javaRow is the subset of a DynamoDB sense_data item worth comparing: every
// field orb also stores.
type javaRow struct {
	temp, hum, light, lightVar, aqr *int64
	apbg, apedb, apd, and, wc, hc   *int64
	offset                          *int64
}

func loadJava(dir string, since time.Time) (map[string]javaRow, error) {
	out := map[string]javaRow{}
	matches, err := filepath.Glob(filepath.Join(dir, "sense_data_*.json"))
	if err != nil {
		return nil, err
	}
	for _, m := range matches {
		f, err := os.Open(m)
		if err != nil {
			return nil, err
		}
		var d struct{ Items []item }
		err = json.NewDecoder(f).Decode(&d)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m, err)
		}
		for _, it := range d.Items {
			key := it.str("ts|dev")
			tsStr, dev := key, ""
			if i := strings.Index(key, "|"); i >= 0 {
				tsStr, dev = key[:i], key[i+1:]
			}
			ts, err := time.Parse("2006-01-02 15:04", tsStr)
			if err != nil {
				continue
			}
			if ts.Before(since) {
				continue
			}
			out[dev+"|"+ts.UTC().Format(time.RFC3339)] = javaRow{
				temp: p(it.num("tmp")), hum: p(it.num("hum")),
				light: p(it.num("lite")), lightVar: p(it.num("litevar")),
				aqr: p(it.num("aqr")), apbg: p(it.num("apbg")),
				apedb: p(it.num("apedb")), apd: p(it.num("apd")),
				and: p(it.num("and")), wc: p(it.num("wc")),
				hc: p(it.num("hc")), offset: p(it.num("om")),
			}
		}
	}
	return out, nil
}

func p(v int64, ok bool) *int64 {
	if !ok {
		return nil
	}
	return &v
}

func main() {
	var (
		dumpDir  = flag.String("dump", "./migrations/dump", "directory of fresh DynamoDB dumps")
		dsn      = flag.String("dsn", "postgres://hello:hello@localhost:5432/orb", "orb Postgres DSN")
		sinceStr = flag.String("since", "", "only compare samples at or after this RFC3339 time (required)")
	)
	flag.Parse()

	if *sinceStr == "" {
		log.Fatal("-since is required: comparing before both systems were writing is meaningless")
	}
	since, err := time.Parse(time.RFC3339, *sinceStr)
	if err != nil {
		log.Fatalf("bad -since: %v", err)
	}

	java, err := loadJava(*dumpDir, since)
	if err != nil {
		log.Fatalf("load java rows: %v", err)
	}

	ctx := context.Background()
	db, err := pgx.Connect(ctx, *dsn)
	if err != nil {
		log.Fatalf("connect orb: %v", err)
	}
	defer db.Close(ctx)

	rows, err := db.Query(ctx, `
		SELECT device_id, ts, offset_ms, temperature, humidity, light, light_variance,
		       air_quality_raw, audio_peak_background_db, audio_peak_energy_db,
		       audio_peak_disturbances_db, audio_num_disturbances, wave_count, hold_count
		FROM sensor_samples WHERE ts >= $1 ORDER BY ts`, since)
	if err != nil {
		log.Fatalf("query orb: %v", err)
	}
	defer rows.Close()

	var compared, identical, mismatched, onlyOrb int
	var diffs []string

	for rows.Next() {
		var dev string
		var ts time.Time
		var offset int32
		var temp, hum, light, lightVar, aqr, apbg, apedb, apd, and, wc, hc *int32
		if err := rows.Scan(&dev, &ts, &offset, &temp, &hum, &light, &lightVar, &aqr,
			&apbg, &apedb, &apd, &and, &wc, &hc); err != nil {
			log.Fatal(err)
		}

		key := dev + "|" + ts.UTC().Format(time.RFC3339)
		j, ok := java[key]
		if !ok {
			onlyOrb++
			continue
		}
		compared++

		var bad []string
		cmp := func(name string, got *int32, want *int64) {
			var g, w any = "null", "null"
			eq := got == nil && want == nil
			if got != nil && want != nil {
				eq = int64(*got) == *want
			}
			if got != nil {
				g = *got
			}
			if want != nil {
				w = *want
			}
			if !eq {
				bad = append(bad, fmt.Sprintf("%s orb=%v java=%v", name, g, w))
			}
		}
		cmp("temperature", temp, j.temp)
		cmp("humidity", hum, j.hum)
		cmp("light", light, j.light)
		cmp("light_variance", lightVar, j.lightVar)
		cmp("air_quality_raw", aqr, j.aqr)
		cmp("audio_peak_background_db", apbg, j.apbg)
		cmp("audio_peak_energy_db", apedb, j.apedb)
		cmp("audio_peak_disturbances_db", apd, j.apd)
		cmp("audio_num_disturbances", and, j.and)
		cmp("wave_count", wc, j.wc)
		cmp("hold_count", hc, j.hc)
		cmp("offset_ms", &offset, j.offset)

		if len(bad) == 0 {
			identical++
		} else {
			mismatched++
			if len(diffs) < 20 {
				diffs = append(diffs, fmt.Sprintf("  %s %s\n    %s",
					dev, ts.UTC().Format(time.RFC3339), strings.Join(bad, "\n    ")))
			}
		}
	}
	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}

	// Rows Java stored that orb never got. Expected to be non-zero only for
	// minutes before orb started writing.
	onlyJava := 0
	seen := map[string]bool{}
	for k := range java {
		seen[k] = true
	}
	onlyJava = len(java) - compared

	fmt.Printf("comparison window: from %s\n\n", since.Format(time.RFC3339))
	fmt.Printf("  compared          %6d\n", compared)
	fmt.Printf("  identical         %6d\n", identical)
	fmt.Printf("  mismatched        %6d\n", mismatched)
	fmt.Printf("  only in orb       %6d\n", onlyOrb)
	fmt.Printf("  only in java      %6d\n", onlyJava)

	if len(diffs) > 0 {
		fmt.Printf("\nfirst %d mismatches:\n", len(diffs))
		sort.Strings(diffs)
		for _, d := range diffs {
			fmt.Println(d)
		}
	}
	if mismatched > 0 {
		os.Exit(1)
	}
}

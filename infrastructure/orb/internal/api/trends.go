package api

import (
	"math"
	"net/http"
	"strings"
	"time"
)

// GET /v2/trends/{scale}: the three graphs on the app's trends screen.
//
// Sleep Score as a calendar grid, Sleep Duration as bars, Sleep Depth as
// bubbles. Everything here is shaping: it reads the same per-night aggregates
// the timeline already stores and decides where each night is drawn. No raw
// samples are involved, so by the seam rule this is Go, not orb-algo.
//
// The whole endpoint is a calendar problem rather than a statistics problem.
// Almost all of the code below is deciding which cell a night belongs in and
// which cells are empty, and there are three different kinds of empty: a null
// (outside the account's life, drawn as nothing), a -1 (a night inside it with
// no data, drawn as a gap), and a value. Conflating null with -1 renders a
// month the user was not yet a customer as a wall of missing nights.

// TrendsResponse is the whole payload.
type TrendsResponse struct {
	AvailableTimeScales []string `json:"available_time_scales"`
	Graphs              []Graph  `json:"graphs"`
}

type Graph struct {
	TimeScale       string           `json:"time_scale"`
	Title           string           `json:"title"`
	DataType        string           `json:"data_type"`
	GraphType       string           `json:"graph_type"`
	MinValue        float32          `json:"min_value"`
	MaxValue        float32          `json:"max_value"`
	Sections        []GraphSection   `json:"sections"`
	ConditionRanges []ConditionRange `json:"condition_ranges"`
	Annotations     []Annotation     `json:"annotations"`
}

// GraphSection is one row of the graph: a week, or a month, or the whole thing.
//
// Values are pointers because null and -1 are different states, see above.
type GraphSection struct {
	Values            []*float32 `json:"values"`
	Titles            []string   `json:"titles"`
	HighlightedValues []int      `json:"highlighted_values"`
	HighlightedTitle  *int       `json:"highlighted_title"`
}

type ConditionRange struct {
	MinValue  float32 `json:"min_value"`
	MaxValue  float32 `json:"max_value"`
	Condition string  `json:"condition"`
}

type Annotation struct {
	Title     string  `json:"title"`
	Value     float32 `json:"value"`
	DataType  string  `json:"data_type"`
	Condition *string `json:"condition"`
}

// missingValue is the reference's GraphSection.MISSING_VALUE: a night that
// should exist and has no data. Distinct from null, which is a cell outside the
// account's life entirely.
const missingValue float32 = -1.0

var (
	dayOfWeekNames   = []string{"SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"}
	monthOfYearNames = []string{"JAN", "FEB", "MAR", "APR", "MAY", "JUN",
		"JUL", "AUG", "SEP", "OCT", "NOV", "DEC"}
)

// timeScale is one of the three windows the app offers.
//
// visibleAfter gates the scale on account age: a week-old account is not
// offered a three month view, because three months of mostly-empty calendar
// reads as a broken screen rather than as a new account.
type timeScale struct {
	name         string
	days         int
	visibleAfter int
}

var timeScales = []timeScale{
	{"LAST_WEEK", 7, 3},
	{"LAST_MONTH", 30, 7},
	{"LAST_3_MONTHS", 90, 30},
}

func timeScaleFrom(s string) (timeScale, bool) {
	for _, ts := range timeScales {
		if strings.EqualFold(s, ts.name) {
			return ts, true
		}
	}
	return timeScale{}, false
}

// The thresholds behind the score graph's coloured bands.
const (
	idealScoreThreshold   = 80
	warningScoreThreshold = 60
	alertScoreThreshold   = 0
	maxScore              = 100
)

var sleepScoreConditionRanges = []ConditionRange{
	{idealScoreThreshold, maxScore, "IDEAL"},
	{warningScoreThreshold, idealScoreThreshold - 1, "WARNING"},
	{alertScoreThreshold, warningScoreThreshold - 1, "ALERT"},
}

// trendsScoreCondition is the reference's Condition.getScoreCondition.
//
// Deliberately not the timeline's scoreCondition, which takes an int and has no
// UNKNOWN: this one takes an average, which is a float, and has a fourth state
// below zero. The two happen to agree on every score a night can actually have,
// and they are different functions in the reference, so they stay different
// here rather than being merged into a shared one that would then have to grow
// a flag.
func trendsScoreCondition(v float32) string {
	switch {
	case v >= idealScoreThreshold:
		return "IDEAL"
	case v >= warningScoreThreshold:
		return "WARNING"
	case v >= alertScoreThreshold:
		return "ALERT"
	default:
		return "UNKNOWN"
	}
}

// Account age gates, all in days.
const (
	minAccountAge         = 3  // no graphs at all below this
	maxAgeShowWelcome     = 14 // a young account with no data gets no time scales either
	absoluteMinDataSize   = 3
	minDataSizeShowMinMax = 3
	annotationEnabledAge  = 7
)

// isoDOW is Monday=1 through Sunday=7, which is what the reference's date
// library uses and what every index below assumes. Go's own Weekday is
// Sunday=0, and the two are off by one in a way that silently rotates the whole
// calendar by a day.
func isoDOW(t time.Time) int {
	if d := int(t.Weekday()); d != 0 {
		return d
	}
	return 7
}

// daysBetween is whole days from a to b, negative when b precedes a. Both are
// civil dates at midnight UTC, so this is subtraction and never a DST question.
func daysBetween(a, b time.Time) int {
	return int(b.Sub(a).Hours() / 24)
}

// civil truncates an instant to the local date it fell on, carried as midnight
// UTC. Every date in this file is one of these: trends never asks what time
// something happened, only what day it was.
//
// The .UTC() is load-bearing and cost a day. Callers have already added the
// account's offset to produce a local-as-UTC instant, so the wall clock to read
// is the UTC one. Without it, pgx hands back a TIMESTAMPTZ in the machine's own
// zone and Date() answers in that zone, applying the offset a second time: this
// account was created at 04:03 UTC, which is 00:03 local, and reading it in a
// -4 zone after the offset was already applied moved the creation date back one
// day. The visible symptom was one cell of a calendar three sections away
// showing a missed night instead of a day before the account existed.
func civil(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func fptr(v float32) *float32 { return &v }
func iptr(v int) *int         { return &v }

func (h *Handler) getTrends(w http.ResponseWriter, r *http.Request) {
	accountID, ok := AccountFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	scale, ok := timeScaleFrom(r.PathValue("scale"))
	if !ok {
		// 400 with the reference's own message, which the app surfaces.
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code: http.StatusBadRequest, Message: "Invalid time-scale."})
		return
	}

	acct, err := h.store.Account(r.Context(), accountID)
	if err != nil {
		h.log.Error("trends account", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	offsetMS, _, err := h.store.TimezoneAt(r.Context(), accountID, time.Now().UTC())
	if err != nil {
		h.log.Error("trends timezone", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	offset := time.Duration(offsetMS) * time.Millisecond
	localToday := civil(time.Now().UTC().Add(offset))
	accountCreated := civil(acct.CreatedAt.Add(offset))
	// Inclusive of both ends, and +1 because an account created today is one day
	// old rather than zero. Every gate below is written against that reading.
	accountAge := daysBetween(accountCreated, localToday) + 1

	rows, err := h.store.TrendsStats(r.Context(), accountID,
		localToday.AddDate(0, 0, -scale.days), localToday)
	if err != nil {
		h.log.Error("trends stats", "account", accountID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	stats := make([]trendsStat, 0, len(rows))
	for _, r := range rows {
		stats = append(stats, trendsStat{
			date: civil(r.Date), durationMins: r.DurationMins,
			light: r.LightMins, medium: r.MediumMins, sound: r.SoundMins,
			score: r.Score,
		})
	}

	writeJSON(w, http.StatusOK, buildTrends(accountAge, accountCreated, localToday, scale, stats))
}

// buildTrends is the whole of the reference's TrendsProcessor.getGraphs.
//
// Split out from the handler so it can be tested against a fixed today: every
// value on this screen depends on what day it is, and a test that calls
// time.Now passes in one week and fails in the next.
func buildTrends(accountAge int, accountCreated, localToday time.Time,
	scale timeScale, stats []trendsStat) TrendsResponse {

	// Below four days there is no screen at all, not even the scale picker.
	if accountAge <= minAccountAge {
		return TrendsResponse{AvailableTimeScales: []string{}, Graphs: []Graph{}}
	}

	available := []string{}
	for _, ts := range timeScales {
		if accountAge > ts.visibleAfter {
			available = append(available, ts.name)
		}
	}

	if len(stats) == 0 {
		// A young account with nothing gets an empty screen rather than a scale
		// picker over three empty graphs. An old account with nothing keeps the
		// picker, because for them an empty week is information.
		if accountAge <= maxAgeShowWelcome {
			return TrendsResponse{AvailableTimeScales: []string{}, Graphs: []Graph{}}
		}
		return TrendsResponse{AvailableTimeScales: available, Graphs: []Graph{}}
	}
	if len(stats) < absoluteMinDataSize {
		return TrendsResponse{AvailableTimeScales: available, Graphs: []Graph{}}
	}

	hasAnnotation := accountAge >= annotationEnabledAge

	graphs := []Graph{
		daysGraph(stats, scale, "GRID", "SCORES", "Sleep Score",
			localToday, accountCreated, hasAnnotation),
		daysGraph(stats, scale, "BAR", "HOURS", "Sleep Duration",
			localToday, accountCreated, hasAnnotation),
		depthGraph(stats, scale),
	}
	return TrendsResponse{AvailableTimeScales: available, Graphs: graphs}
}

// trendsStat is the handler's view of a night, decoupled from the store row so
// buildTrends can be driven from a table in a test.
type trendsStat struct {
	date                               time.Time
	durationMins, light, medium, sound int32
	score                              int32
}

// depthGraph is the bubbles: what share of all sleep was light, medium, deep.
//
// A ratio over the whole window rather than a per-night average, so a long
// night counts for more than a short one. Empty sections when there is no sleep
// at all, rather than three NaNs.
func depthGraph(stats []trendsStat, scale timeScale) Graph {
	var totalSleep, totalLight, totalMedium, totalSound float32
	for _, s := range stats {
		totalLight += float32(s.light)
		totalSound += float32(s.sound)
		totalMedium += float32(s.medium)
		totalSleep += float32(s.light) + float32(s.sound) + float32(s.medium)
	}

	sections := []GraphSection{}
	if totalSleep > 0 {
		sections = append(sections, GraphSection{
			Values: []*float32{
				fptr(totalLight / totalSleep),
				fptr(totalMedium / totalSleep),
				fptr(totalSound / totalSleep),
			},
			// "DEEP" on screen, `sound` everywhere in the data. The label and
			// the column have never had the same name.
			Titles:            []string{"LIGHT", "MEDIUM", "DEEP"},
			HighlightedValues: []int{},
			HighlightedTitle:  nil,
		})
	}

	return Graph{
		TimeScale: scale.name, Title: "Sleep Depth",
		DataType: "PERCENTS", GraphType: "BUBBLES",
		MinValue: 0.0, MaxValue: 1.0,
		Sections:        sections,
		ConditionRanges: []ConditionRange{},
		Annotations:     []Annotation{},
	}
}

// daysGraph builds the two graphs whose x-axis is days: score and duration.
func daysGraph(stats []trendsStat, scale timeScale, graphType, dataType, title string,
	localToday, accountCreated time.Time, hasAnnotation bool) Graph {

	var ann annotationStats

	// Float32's smallest positive value as the initial maximum, from the
	// reference. It is wrong in principle (a graph whose values are all zero
	// reports a maximum of 1.4e-45) and right for every real input, because
	// durations are positive and this graph is not drawn below three nights.
	// Reproduced rather than corrected: it is visible in min_value/max_value.
	maxValue := float32(math.SmallestNonzeroFloat32)
	minValue := float32(math.MaxFloat32)

	valid := []*float32{}
	for i, s := range stats {
		var v float32
		if dataType == "HOURS" {
			v = float32(s.durationMins) / 60.0
		} else {
			v = float32(s.score)
		}

		// Fill gaps between consecutive nights, so a week with a hole in it
		// keeps its shape. These are -1 rather than null: the account existed,
		// the night did not record.
		if i > 0 {
			if diff := daysBetween(stats[i-1].date, s.date); diff > 1 {
				for day := 1; day < diff; day++ {
					valid = append(valid, fptr(missingValue))
				}
			}
		}
		valid = append(valid, fptr(v))

		if hasAnnotation {
			// Friday is 5, so Saturday and Sunday are the weekend. The reference
			// counts by ISO day number, which puts the boundary here.
			if isoDOW(s.date) <= 5 {
				ann.sumWeekday += v
				ann.numWeekdays++
			} else {
				ann.sumWeekend += v
				ann.numWeekends++
			}
			ann.sumValues += v
			ann.numDays++
		}

		minValue = min(minValue, v)
		maxValue = max(maxValue, v)
	}

	// The score grid is always a calendar, so it pads to whole weeks. Duration
	// only does for the week view; over a month it is a run of bars split by
	// month boundaries, where a day-of-week alignment would mean nothing.
	padDayOfWeek := scale.name == "LAST_WEEK" ||
		(dataType == "SCORES" && scale.name == "LAST_MONTH")

	sectionData := padSectionData(valid, localToday, stats[0].date,
		stats[len(stats)-1].date, scale, padDayOfWeek, accountCreated)

	hasMinMax := len(stats) >= minDataSizeShowMinMax
	if !hasMinMax {
		minValue = 0.0
	}

	var sections []GraphSection
	if dataType == "SCORES" {
		if scale.name != "LAST_3_MONTHS" {
			sections = scoreWeekSections(sectionData, localToday)
		} else {
			sections = scoreThreeMonthsSections(sectionData, scale, localToday)
		}
	} else if scale.name == "LAST_WEEK" {
		sections = durationWeekSection(sectionData, minValue, maxValue, localToday, hasMinMax)
	} else {
		sections = durationMonthSections(sectionData, minValue, maxValue, scale, localToday, hasMinMax)
	}

	annotations := []Annotation{}
	if hasAnnotation {
		annotations = buildAnnotations(ann, dataType)
	}

	conditionRanges := []ConditionRange{}
	if dataType == "SCORES" {
		conditionRanges = append(conditionRanges, sleepScoreConditionRanges...)
	}

	return Graph{
		TimeScale: scale.name, Title: title,
		DataType: dataType, GraphType: graphType,
		MinValue: minValue, MaxValue: maxValue,
		Sections:        sections,
		ConditionRanges: conditionRanges,
		Annotations:     annotations,
	}
}

type annotationStats struct {
	sumValues, sumWeekday, numWeekdays, sumWeekend, numWeekends, numDays float32
}

// buildAnnotations is the three averages under the graph.
//
// All three or none: the reference drops the lot when it cannot produce a full
// set, and a week worked entirely on weekdays produces only two. That is why a
// real response can carry an empty annotations list on an account well past the
// age gate, which otherwise looks like a bug.
func buildAnnotations(s annotationStats, dataType string) []Annotation {
	out := []Annotation{}
	add := func(title string, sum, n float32) {
		if n <= 0 {
			return
		}
		// Rounded to one decimal by the round-trip through tenths, which is what
		// the reference does; the value is a float and the app prints it raw.
		avg := float32(math.Round(float64(sum/n)*10.0)) / 10.0
		a := Annotation{Title: title, Value: avg, DataType: dataType}
		if dataType == "SCORES" {
			c := trendsScoreCondition(avg)
			a.Condition = &c
		}
		out = append(out, a)
	}
	add("Avg. Weekdays", s.sumWeekday, s.numWeekdays)
	add("Avg. Weekends", s.sumWeekend, s.numWeekends)
	add("Avg. Overall", s.sumValues, s.numDays)

	if len(out) < 3 {
		return []Annotation{}
	}
	return out
}

// padSectionData turns the run of nights into a fixed calendar window.
//
// The two kinds of padding are the whole point. Days before the account existed
// are null; days inside its life with no night are -1. The account creation
// date is what separates them, and without it a new user's first week is drawn
// as six failures.
func padSectionData(data []*float32, today, firstData, lastData time.Time,
	scale timeScale, padDayOfWeek bool, accountCreated time.Time) []*float32 {

	out := []*float32{}
	firstDate := today.AddDate(0, 0, -scale.days)

	// In the week view, starting before the account existed would add a whole
	// leading row of nulls, so the window is clamped forward instead.
	if scale.name == "LAST_WEEK" && firstDate.Before(accountCreated) {
		firstDate = accountCreated
	}
	// An account created just after midnight has its first night dated the day
	// before it existed, so the window has to stretch back to cover it.
	if firstData.Before(firstDate) {
		firstDate = firstData
	}

	if missing := daysBetween(firstDate, firstData); missing > 0 {
		for day := 0; day < missing; day++ {
			thisDay := firstDate.AddDate(0, 0, day)
			if thisDay.Before(accountCreated) {
				out = append([]*float32{nil}, out...)
			} else {
				out = append(out, fptr(missingValue))
			}
		}
	}

	// Align the start to a Sunday so the grid's columns are days of the week.
	if padDayOfWeek {
		if dow := isoDOW(firstDate); dow < 7 {
			for day := 0; day < dow; day++ {
				out = append([]*float32{nil}, out...)
			}
		}
	}

	out = append(out, data...)

	// Nights between the last data and yesterday are real gaps. Today is never
	// included: its night has not happened yet.
	if missing := daysBetween(lastData, today.AddDate(0, 0, -1)); missing > 0 {
		for day := 0; day < missing; day++ {
			out = append(out, fptr(missingValue))
		}
	}

	if padDayOfWeek {
		if dow := isoDOW(today); dow < 7 {
			for day := dow; day < 7; day++ {
				out = append(out, nil)
			}
		}
	}
	return out
}

// scoreWeekSections cuts the calendar into weeks, one section per row.
//
// Only the first row carries day names, and only the last row highlights today.
// numWeeks is a truncating division, so a trailing partial week is not counted
// and therefore never gets the highlight.
func scoreWeekSections(data []*float32, today time.Time) []GraphSection {
	sections := []GraphSection{}
	numWeeks := len(data) / 7
	todayDOW := isoDOW(today)

	weeks := 0
	for start := 0; start < len(data); start += 7 {
		end := min(start+7, len(data))
		oneWeek := data[start:end]
		weeks++

		var highlightedTitle *int
		titles := []string{}
		if weeks == 1 {
			highlightedTitle = iptr(todayDOW - 1)
			titles = dayOfWeekNames
			if numWeeks > 1 {
				sections = append(sections, GraphSection{
					Values: oneWeek, Titles: titles,
					HighlightedValues: []int{}, HighlightedTitle: highlightedTitle,
				})
				continue
			}
		}

		highlightedValues := []int{}
		if weeks == numWeeks {
			highlightedValues = append(highlightedValues, todayDOW-1)
		}
		sections = append(sections, GraphSection{
			Values: oneWeek, Titles: titles,
			HighlightedValues: highlightedValues, HighlightedTitle: highlightedTitle,
		})
	}
	return sections
}

// durationWeekSection is the one-row bar chart for the week view.
//
// Nulls are dropped rather than drawn, then the row is padded back to seven
// from the left, which is why the highlight indices have to move with it.
func durationWeekSection(data []*float32, minValue, maxValue float32,
	today time.Time, hasMinMax bool) []GraphSection {

	titles := []string{}
	for day := 7; day >= 1; day-- {
		dow := isoDOW(today.AddDate(0, 0, -day))
		if dow == 7 {
			dow = 0
		}
		titles = append(titles, dayOfWeekNames[dow])
	}

	values := []*float32{}
	minIndex, maxIndex, index := -1, -1, 0
	for _, v := range data {
		if v == nil {
			continue
		}
		if hasMinMax {
			// The last minimum and the first maximum, deliberately asymmetric:
			// the min branch keeps overwriting, the max branch is guarded.
			if *v == minValue {
				minIndex = index
			} else if maxIndex == -1 && *v == maxValue {
				maxIndex = index
			}
		}
		values = append(values, v)
		index++
	}

	for i := 7 - len(values); i > 0; i-- {
		values = append([]*float32{fptr(missingValue)}, values...)
		if minIndex >= 0 {
			minIndex++
		}
		if maxIndex >= 0 {
			maxIndex++
		}
	}

	// One night cannot be both the best and the worst; when it is, the graph
	// shows only the best.
	if maxIndex == minIndex {
		minIndex = -1
	}

	highlights := []int{}
	if minIndex >= 0 {
		highlights = append(highlights, minIndex)
	}
	if maxIndex >= 0 {
		highlights = append(highlights, maxIndex)
	}

	return []GraphSection{{
		Values: values, Titles: titles, HighlightedValues: highlights,
		// Always the last column, which is yesterday: the week view ends on the
		// most recent completed night rather than on today.
		HighlightedTitle: iptr(6),
	}}
}

// durationMonthSections splits the bars at month boundaries, one section per
// calendar month, titled with the month name.
func durationMonthSections(data []*float32, minValue, maxValue float32,
	scale timeScale, today time.Time, hasMinMax bool) []GraphSection {

	values := []*float32{}
	minIndex, maxIndex := -1, -1
	for index, v := range data {
		if v != nil {
			if *v == minValue {
				minIndex = index
			} else if maxIndex == -1 && *v == maxValue {
				maxIndex = index
			}
			values = append(values, v)
		} else {
			values = append(values, fptr(missingValue))
		}
	}
	if minIndex == maxIndex {
		minIndex = -1
	}

	sections := []GraphSection{}
	firstDate := today.AddDate(0, 0, -scale.days)
	title := monthOfYearNames[int(firstDate.Month())-1]
	sectionFirstIndex, added := 0, 0

	for day := 0; day < scale.days; day++ {
		monthName := monthOfYearNames[int(firstDate.AddDate(0, 0, day).Month())-1]
		if title == monthName {
			continue
		}
		highlights := []int{}
		if hasMinMax {
			// Each highlight is consumed by the first section that contains it,
			// which is what the reset to -1 is for.
			if minIndex >= 0 && minIndex < day {
				highlights = append(highlights, minIndex-sectionFirstIndex)
				minIndex = -1
			}
			if maxIndex >= 0 && maxIndex < day {
				highlights = append(highlights, maxIndex-sectionFirstIndex)
				maxIndex = -1
			}
		}
		sections = append(sections, GraphSection{
			Values: values[sectionFirstIndex:day], Titles: []string{title},
			HighlightedValues: highlights, HighlightedTitle: nil,
		})
		added += day - sectionFirstIndex
		sectionFirstIndex = day
		title = monthName
	}

	lastDay := scale.days - 1
	if added < len(values) {
		highlights := []int{}
		if hasMinMax {
			if minIndex >= 0 && minIndex <= lastDay {
				highlights = append(highlights, minIndex-sectionFirstIndex)
			}
			if maxIndex >= 0 && maxIndex <= lastDay {
				highlights = append(highlights, maxIndex-sectionFirstIndex)
			}
		}
		end := min(lastDay+1, len(values))
		sections = append(sections, GraphSection{
			Values: values[sectionFirstIndex:end], Titles: []string{title},
			HighlightedValues: highlights, HighlightedTitle: nil,
		})
	}
	return sections
}

// scoreThreeMonthsSections is the calendar view over three months: one section
// per month, each a whole month from the 1st to the last day.
//
// Unverified against the reference. The account this was built against is under
// thirty days old, so LAST_3_MONTHS is not offered to it and apidiff cannot
// reach this path. It is a transcription of the reference, not a checked one.
func scoreThreeMonthsSections(data []*float32, scale timeScale, today time.Time) []GraphSection {
	sectionData := []*float32{}

	// Pad back to the 1st of the first month, so every section is a full
	// calendar month and the columns line up.
	firstDate := today.AddDate(0, 0, -scale.days)
	if firstDate.Day() != 1 {
		for day := 0; day < firstDate.Day()-1; day++ {
			sectionData = append(sectionData, nil)
		}
	}
	sectionData = append(sectionData, data...)

	// Pad forward to the end of the current month.
	lastDate := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC).
		AddDate(0, 1, -1)
	if today.Day() > 1 && today.Day() <= lastDate.Day() {
		for day := 0; day <= lastDate.Day()-today.Day(); day++ {
			sectionData = append(sectionData, nil)
		}
	}

	sections := []GraphSection{}
	numMonths := monthsBetween(firstDate, lastDate) + 1
	sectionFirstIndex := 0
	highlighted := false

	for month := 0; month < numMonths; month++ {
		currentMonth := firstDate.AddDate(0, month, 0)

		highlightedValues := []int{}
		var highlightTitle *int
		// The highlight only ever appears in the last month, and on the 1st of a
		// month it belongs to the month before, because yesterday was.
		if !highlighted && (month == numMonths-1 ||
			(today.Day() == 1 && month == numMonths-2)) {
			highlightedValues = append(highlightedValues, today.AddDate(0, 0, -1).Day()-1)
			highlightTitle = iptr(0)
			highlighted = true
		}

		maxDays := time.Date(currentMonth.Year(), currentMonth.Month(), 1, 0, 0, 0, 0, time.UTC).
			AddDate(0, 1, -1).Day()
		lastIndex := min(sectionFirstIndex+maxDays, len(sectionData))
		if sectionFirstIndex > lastIndex {
			sectionFirstIndex = lastIndex
		}
		sections = append(sections, GraphSection{
			Values:            sectionData[sectionFirstIndex:lastIndex],
			Titles:            []string{monthOfYearNames[int(currentMonth.Month())-1]},
			HighlightedValues: highlightedValues, HighlightedTitle: highlightTitle,
		})
		sectionFirstIndex += maxDays
	}
	return sections
}

// monthsBetween is whole months from a to b, matching the reference's date
// library: a partial month does not count.
func monthsBetween(a, b time.Time) int {
	months := (b.Year()-a.Year())*12 + int(b.Month()) - int(a.Month())
	if b.Day() < a.Day() {
		months--
	}
	return months
}

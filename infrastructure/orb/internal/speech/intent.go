package speech

import (
	"regexp"
	"strings"
)

// Intent is the kind of thing a transcript is asking for. The set mirrors the
// supichi handlers that answer from data orb already has; the smart-home ones
// (Hue, Nest) are deliberately absent because those clouds are gone.
type Intent int

const (
	IntentUnknown Intent = iota
	IntentTime
	IntentTemperature
	IntentHumidity
	IntentAirQuality
	IntentLight
	IntentSleepScore
	IntentAlarmSet
	IntentAlarmQuery
	IntentAlarmCancel
	IntentSleepSoundPlay
	IntentSleepSoundStop
)

// Match is a classified transcript: the intent and any slot it carried (so far
// only an alarm time).
type Match struct {
	Intent    Intent
	AlarmHour int // 24h, valid only for IntentAlarmSet
	AlarmMin  int
}

// The patterns are the supichi handlers' triggers, loosened to match natural
// phrasing. Order matters: the first hit wins, so the more specific patterns
// (alarms with a time, stop/play) come before the bare topic words.
var (
	reAlarmCancel = regexp.MustCompile(`\b(cancel|delete|remove|turn off)\b.*\balarm`)
	reAlarmQuery  = regexp.MustCompile(`\b(when|what time)\b.*\balarm|\balarm\b.*\b(set|next)\b`)
	reAlarmSet    = regexp.MustCompile(`\b(set|wake me|create).*\balarm|\balarm\b.*\bfor\b|wake me up`)
	reTime        = regexp.MustCompile(`\bwhat('?s| is)?\s+the\s+time\b|\bwhat time is it\b`)
	reTemp        = regexp.MustCompile(`\btemperature\b|\bhow (warm|hot|cold)\b|\bis it (warm|hot|cold)\b`)
	reHumidity    = regexp.MustCompile(`\bhumid`)
	reAir         = regexp.MustCompile(`\bair quality\b|\bair\b|\bparticulate|\bdust\b`)
	reLight       = regexp.MustCompile(`\bhow (bright|dark)\b|\blight level\b|\bhow much light\b`)
	reSleepScore  = regexp.MustCompile(`\bsleep score\b|\bhow did i sleep\b|\bsleep summary\b|\bhow was my sleep\b`)
	reSoundStop   = regexp.MustCompile(`\bstop\b.*\b(playing|sound|noise)|\bstop playing\b|\bturn off\b.*\b(sound|noise|music)`)
	reSoundPlay   = regexp.MustCompile(`\bplay\b.*(sound|noise|rain|white noise|music)|\bplay (some )?(white noise|rain)`)

	reTimeSlot = regexp.MustCompile(`\b(\d{1,2})(?::(\d{2}))?\s*(a\.?m\.?|p\.?m\.?)?\b`)
)

// Classify maps a transcript to an intent. It lowercases and matches against
// the handler patterns; an unrecognized transcript is IntentUnknown, which the
// caller answers with a "sorry, I can't help with that" reply.
func Classify(transcript string) Match {
	t := strings.ToLower(strings.TrimSpace(transcript))

	switch {
	case reAlarmCancel.MatchString(t):
		return Match{Intent: IntentAlarmCancel}
	case reAlarmSet.MatchString(t):
		m := Match{Intent: IntentAlarmSet}
		m.AlarmHour, m.AlarmMin = parseAlarmTime(t)
		return m
	case reAlarmQuery.MatchString(t):
		return Match{Intent: IntentAlarmQuery}
	case reSoundStop.MatchString(t):
		return Match{Intent: IntentSleepSoundStop}
	case reSoundPlay.MatchString(t):
		return Match{Intent: IntentSleepSoundPlay}
	case reSleepScore.MatchString(t):
		return Match{Intent: IntentSleepScore}
	case reTime.MatchString(t):
		return Match{Intent: IntentTime}
	case reHumidity.MatchString(t):
		return Match{Intent: IntentHumidity}
	case reTemp.MatchString(t):
		return Match{Intent: IntentTemperature}
	case reAir.MatchString(t):
		return Match{Intent: IntentAirQuality}
	case reLight.MatchString(t):
		return Match{Intent: IntentLight}
	}
	return Match{Intent: IntentUnknown}
}

// parseAlarmTime pulls "7", "7am", "6:30", "6:30 pm" out of a set-alarm
// transcript. Returns (-1,-1) when no time is present, which the caller treats
// as an incomplete request. Hour is normalized to 24h.
func parseAlarmTime(t string) (int, int) {
	// Skip a leading "for" so "set an alarm for 7" anchors on the 7, not a
	// stray number elsewhere; the regex already prefers the first time token.
	m := reTimeSlot.FindStringSubmatch(t)
	if m == nil {
		return -1, -1
	}
	hour := atoi(m[1])
	if hour < 0 || hour > 23 {
		return -1, -1
	}
	min := 0
	if m[2] != "" {
		min = atoi(m[2])
	}
	ampm := strings.ReplaceAll(m[3], ".", "")
	switch ampm {
	case "pm":
		if hour < 12 {
			hour += 12
		}
	case "am":
		if hour == 12 {
			hour = 0
		}
	}
	if min < 0 || min > 59 {
		return -1, -1
	}
	return hour, min
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}

package speech

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		in   string
		want Intent
	}{
		{"What time is it?", IntentTime},
		{"what's the time", IntentTime},
		{"What's the temperature?", IntentTemperature},
		{"is it warm in here", IntentTemperature},
		{"how humid is it", IntentHumidity},
		{"how's the air quality", IntentAirQuality},
		{"how bright is it", IntentLight},
		{"what was my sleep score", IntentSleepScore},
		{"how did I sleep", IntentSleepScore},
		{"set an alarm for 7 am", IntentAlarmSet},
		{"wake me up at 6:30", IntentAlarmSet},
		{"when is my next alarm", IntentAlarmQuery},
		{"cancel my alarm", IntentAlarmCancel},
		{"play white noise", IntentSleepSoundPlay},
		{"stop playing", IntentSleepSoundStop},
		{"what's the meaning of life", IntentUnknown},
	}
	for _, c := range cases {
		if got := Classify(c.in).Intent; got != c.want {
			t.Errorf("Classify(%q).Intent = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestAlarmTime(t *testing.T) {
	cases := []struct {
		in         string
		hour, mins int
	}{
		{"set an alarm for 7 am", 7, 0},
		{"set an alarm for 7 pm", 19, 0},
		{"wake me up at 6:30 am", 6, 30},
		{"set an alarm for 12 am", 0, 0},
		{"set an alarm for 12 pm", 12, 0},
		{"wake me up at 10:15 pm", 22, 15},
	}
	for _, c := range cases {
		m := Classify(c.in)
		if m.Intent != IntentAlarmSet {
			t.Errorf("%q not classified as alarm set", c.in)
			continue
		}
		if m.AlarmHour != c.hour || m.AlarmMin != c.mins {
			t.Errorf("%q -> %02d:%02d, want %02d:%02d", c.in, m.AlarmHour, m.AlarmMin, c.hour, c.mins)
		}
	}
}

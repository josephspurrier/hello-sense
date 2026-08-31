package api

// The voice command catalog served at GET /v2/voice/commands. The original
// lived in a database table that did not survive; this reconstructs it from
// the capabilities the Sense with Voice actually shipped (its command
// handlers). Each phrase is one the voice pipeline is meant to answer, so this
// list and the handlers implemented server-side are kept in step: a topic
// here without a handler is a promise the device cannot keep.
//
// Shape is suripu's VoiceCommandResponse: topics, each with subtopics, each
// with example phrases. icon_urls is null; the app renders a default glyph.

type voiceCommandsResponse struct {
	Topics []voiceTopic `json:"voice_command_topics"`
}

type voiceTopic struct {
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Subtopics   []voiceSubtopic `json:"subtopics"`
	IconURLs    *RemoteImage    `json:"icon_urls"`
}

type voiceSubtopic struct {
	CommandTitle string   `json:"command_title"`
	Commands     []string `json:"commands"`
}

var voiceCommandCatalog = voiceCommandsResponse{Topics: []voiceTopic{
	{
		Title:       "Sleep",
		Description: "Ask about how you slept",
		Subtopics: []voiceSubtopic{
			{CommandTitle: "Sleep score", Commands: []string{
				"How did I sleep?",
				"What was my sleep score?",
			}},
		},
	},
	{
		Title:       "Alarms",
		Description: "Set and check your alarms",
		Subtopics: []voiceSubtopic{
			{CommandTitle: "Set an alarm", Commands: []string{
				"Set an alarm for 7 AM",
				"Wake me up at 6:30 tomorrow",
			}},
			{CommandTitle: "Check your alarm", Commands: []string{
				"When is my next alarm?",
			}},
			{CommandTitle: "Cancel an alarm", Commands: []string{
				"Cancel my alarm",
			}},
		},
	},
	{
		Title:       "Room Conditions",
		Description: "Check your bedroom's environment",
		Subtopics: []voiceSubtopic{
			{CommandTitle: "Temperature", Commands: []string{
				"What's the temperature?",
				"Is it warm in here?",
			}},
			{CommandTitle: "Humidity", Commands: []string{
				"What's the humidity?",
			}},
			{CommandTitle: "Air quality", Commands: []string{
				"How's the air quality?",
			}},
			{CommandTitle: "Light", Commands: []string{
				"How bright is it?",
			}},
		},
	},
	{
		Title:       "Time",
		Description: "Ask the time",
		Subtopics: []voiceSubtopic{
			{CommandTitle: "Current time", Commands: []string{
				"What time is it?",
			}},
		},
	},
	{
		Title:       "Sleep Sounds",
		Description: "Play sounds to help you sleep",
		Subtopics: []voiceSubtopic{
			{CommandTitle: "Play a sound", Commands: []string{
				"Play white noise",
				"Play rain",
			}},
			{CommandTitle: "Stop", Commands: []string{
				"Stop playing",
			}},
		},
	},
}}

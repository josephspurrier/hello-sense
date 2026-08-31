package api

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/josephspurrier/hello-orb/orb/internal/messeji"
)

// The conversion table is suripu's own worked examples
// (SleepSoundsResource.convertToSenseVolumePercent): perceived loudness
// halves every 10 dB down from the 60 dB ceiling.
func TestSenseVolumePercent(t *testing.T) {
	for _, c := range []struct {
		app   int
		sense uint32
	}{
		{100, 100}, {50, 83}, {25, 67}, {1, 0}, {0, 0},
	} {
		if got := senseVolumePercent(c.app); got != c.sense {
			t.Errorf("senseVolumePercent(%d) = %d, want %d", c.app, got, c.sense)
		}
	}
}

// Every sleep sound resolves to the SD path both `file_info` generations
// record for it; the play command sends this string verbatim.
func TestSleepSoundDevicePaths(t *testing.T) {
	want := map[string]string{
		"Aura": "/SLPTONES/ST010.RAW", "Nocturne": "/SLPTONES/ST012.RAW",
		"Morpheus": "/SLPTONES/ST009.RAW", "Horizon": "/SLPTONES/ST011.RAW",
		"Cosmos": "/SLPTONES/ST002.RAW", "Autumn Wind": "/SLPTONES/ST003.RAW",
		"Fireside": "/SLPTONES/ST004.RAW", "Rainfall": "/SLPTONES/ST006.RAW",
		"Forest Creek": "/SLPTONES/ST008.RAW", "Brown Noise": "/SLPTONES/ST001.RAW",
		"White Noise": "/SLPTONES/ST007.RAW",
	}
	for _, s := range sleepSounds {
		if got := s.devicePath(); got != want[s.Name] {
			t.Errorf("%q plays %q, want %q", s.Name, got, want[s.Name])
		}
	}
}

// The wire bytes the device decodes, checked field by field against the
// firmware's nanopb tables (messeji.pb.c, audio_commands.pb.c). A golden
// byte string rather than a round-trip through our own encoder, because the
// encoder testing itself proves nothing.
func TestPlayAudioBatchWire(t *testing.T) {
	got := messeji.PlayAudioBatch("/A.RAW", 83, 600, 2, 3)
	want := []byte{
		0x0a, 0x1b, // BatchMessage.message, 27 bytes
		0x10, 0x02, // order = 2
		0x18, 0x03, // message_id = 3
		0x20, 0x00, // type = PLAY_AUDIO
		0x2a, 0x13, // play_audio, 19 bytes
		0x0a, 0x06, '/', 'A', '.', 'R', 'A', 'W', // file_path
		0x10, 0x53, // volume_percent = 83
		0x18, 0xd8, 0x04, // duration_seconds = 600
		0x20, 0x01, // fade_in = 1
		0x28, 0x01, // fade_out = 1
		0x30, 0x14, // timeout_fade_out = 20
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PlayAudioBatch = % x\nwant % x", got, want)
	}

	// Indefinitely: duration_seconds omitted entirely, which the firmware
	// reads as play-until-stopped. Not zero, which would be a zero-second
	// play.
	indefinite := messeji.PlayAudioBatch("/A.RAW", 83, 0, 2, 3)
	for i := 0; i+1 < len(indefinite); i++ {
		if indefinite[i] == 0x2a { // play_audio submessage
			sub := indefinite[i+2:]
			for j := 0; j < len(sub); {
				if sub[j] == 0x18 {
					t.Fatalf("indefinite play carries duration_seconds: % x", indefinite)
				}
				// Skip tag+varint pairs and the length-prefixed file_path.
				if sub[j] == 0x0a {
					j += 2 + int(sub[j+1])
					continue
				}
				j += 2
			}
			break
		}
	}
}

func TestStopAudioBatchWire(t *testing.T) {
	got := messeji.StopAudioBatch(5, 7)
	want := []byte{
		0x0a, 0x0a, // BatchMessage.message, 10 bytes
		0x10, 0x05, // order = 5
		0x18, 0x07, // message_id = 7
		0x20, 0x01, // type = STOP_AUDIO
		0x32, 0x02, // stop_audio, 2 bytes
		0x08, 0x01, // fade_out = 1
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("StopAudioBatch = % x\nwant % x", got, want)
	}
}

// The status is read back from what the DEVICE says it is doing (the
// recorded SenseState blob), not from what we last commanded.
func TestStatusFromSenseState(t *testing.T) {
	r := httptest.NewRequest("GET", "https://sense.example.com:8443/v2/sleep_sounds/status", nil)

	for _, c := range []struct {
		name, blob string
		playing    bool
		sound      string
		duration   string
	}{
		{"playing a tone", `{"audioState":{"playingAudio":true,"durationSeconds":600,"filePath":"/SLPTONES/ST001.RAW"}}`,
			true, "Brown Noise", "10 Minutes"},
		{"indefinite as firmware -1", `{"audioState":{"playingAudio":true,"durationSeconds":4294967295,"filePath":"/SLPTONES/ST010.RAW"}}`,
			true, "Aura", "Indefinitely"},
		{"indefinite as omitted", `{"audioState":{"playingAudio":true,"filePath":"/SLPTONES/ST007.RAW"}}`,
			true, "White Noise", "Indefinitely"},
		{"stopped", `{"audioState":{"playingAudio":false}}`, false, "", ""},
		{"no audio state", `{}`, false, "", ""},
		{"an alarm ringing is not a sleep sound", `{"audioState":{"playingAudio":true,"durationSeconds":60,"filePath":"/RINGTONE/DIG002.raw"}}`,
			false, "", ""},
		{"garbage", `not json`, false, "", ""},
	} {
		st := statusFromSenseState(r, []byte(c.blob))
		if st.Playing != c.playing {
			t.Errorf("%s: playing = %v, want %v", c.name, st.Playing, c.playing)
			continue
		}
		if !c.playing {
			if st.Sound != nil || st.Duration != nil {
				t.Errorf("%s: not-playing status carries sound/duration", c.name)
			}
			continue
		}
		if st.Sound == nil || st.Sound.Name != c.sound {
			t.Errorf("%s: sound = %+v, want %q", c.name, st.Sound, c.sound)
		} else if !strings.HasPrefix(st.Sound.PreviewURL, "https://sense.example.com:8443"+soundPreviewPath) {
			t.Errorf("%s: preview_url = %q, not built from the request", c.name, st.Sound.PreviewURL)
		}
		if st.Duration == nil || st.Duration.Name != c.duration {
			t.Errorf("%s: duration = %+v, want %q", c.name, st.Duration, c.duration)
		}
	}
}

// Package messeji encodes the server-to-device command messages that travel
// over the /receive long-poll (the channel that replaces Hello's messeji
// service). The device applies what they carry: play a sleep sound, stop it,
// set the speaker volume.
//
// Rather than pull the whole messeji proto into orb, the three message shapes
// we send are hand-encoded: their field numbers are fixed by the proto and
// small. The shapes below are transcribed from the firmware's own decoder
// (kitsune protobuf/messeji.pb.c and audio_commands.pb.c), which is the
// authority on what the device accepts:
//
//	BatchMessage { repeated Message message = 1 }
//	Message { string sender_id = 1; int64 order = 2 (req);
//	          int64 message_id = 3; Type type = 4 (req);
//	          PlayAudio play_audio = 5; StopAudio stop_audio = 6;
//	          Volume volume = 7 }
//	Type { PLAY_AUDIO = 0; STOP_AUDIO = 1; SET_VOLUME = 2; VOICE_CONTROL = 3 }
//	VoiceControl { bool enable = 1 }   // false => firmware disable_voice = true
//	PlayAudio { string file_path = 1 (req); uint32 volume_percent = 2 (req);
//	            uint32 duration_seconds = 3; uint32 fade_in_duration_seconds = 4 (req);
//	            uint32 fade_out_duration_seconds = 5 (req);
//	            uint32 timeout_fade_out_duration_seconds = 6 }
//	Volume { uint32 volume = 1 (req) }   // 0-100 percent
//	StopAudio { uint32 fade_out_duration_seconds = 1 (req) }
//
// The 1.9.2 firmware dispatches on which submessage is present, not on the
// type enum, but type is a required field so it is always written. The encoded
// batch is not the wire payload by itself: the device validates the long-poll
// response like a sync response, so the caller signs it (sense.Sign) before it
// is sent or queued.
package messeji

import "encoding/binary"

const (
	typePlayAudio    = 0
	typeStopAudio    = 1
	typeSetVolume    = 2
	typeVoiceControl = 3
)

// The fade constants are suripu's: one second in and out feels deliberate
// rather than abrupt, and the 20 second fade on a duration timeout is what
// lets a sleep sound end without waking anyone.
const (
	fadeSeconds        = 1
	timeoutFadeSeconds = 20
)

// PlayAudioBatch encodes a signed-ready BatchMessage telling the device to
// play a file from its SD card. durationSeconds 0 means play until stopped
// (the field is omitted, which the firmware reads as indefinite). order is
// the client's ordering value (the app sends epoch millis); messageID lets
// the device acknowledge on its next poll.
func PlayAudioBatch(filePath string, volumePercent uint32, durationSeconds uint32, order, messageID int64) []byte {
	var play []byte
	play = appendLenField(play, 1, []byte(filePath))
	play = appendField(play, 2, wireVarint, uint64(volumePercent))
	if durationSeconds > 0 {
		play = appendField(play, 3, wireVarint, uint64(durationSeconds))
	}
	play = appendField(play, 4, wireVarint, fadeSeconds)
	play = appendField(play, 5, wireVarint, fadeSeconds)
	play = appendField(play, 6, wireVarint, timeoutFadeSeconds)

	var msg []byte
	msg = appendField(msg, 2, wireVarint, uint64(order))
	msg = appendField(msg, 3, wireVarint, uint64(messageID))
	msg = appendField(msg, 4, wireVarint, typePlayAudio)
	msg = appendLenField(msg, 5, play)

	return appendLenField(nil, 1, msg)
}

// StopAudioBatch encodes a signed-ready BatchMessage telling the device to
// stop playback, fading out over a second.
func StopAudioBatch(order, messageID int64) []byte {
	stop := appendField(nil, 1, wireVarint, fadeSeconds)

	var msg []byte
	msg = appendField(msg, 2, wireVarint, uint64(order))
	msg = appendField(msg, 3, wireVarint, uint64(messageID))
	msg = appendField(msg, 4, wireVarint, typeStopAudio)
	msg = appendLenField(msg, 6, stop)

	return appendLenField(nil, 1, msg)
}

// VolumeBatch encodes a signed-ready BatchMessage carrying one SET_VOLUME
// command. volume is a percent (0-100).
func VolumeBatch(volume uint32, messageID int64) []byte {
	vol := appendField(nil, 1, wireVarint, uint64(volume))

	var msg []byte
	msg = appendField(msg, 2, wireVarint, 1) // order
	msg = appendField(msg, 3, wireVarint, uint64(messageID))
	msg = appendField(msg, 4, wireVarint, typeSetVolume)
	msg = appendLenField(msg, 7, vol)

	return appendLenField(nil, 1, msg)
}

// VoiceControlBatch encodes a signed-ready BatchMessage that enables or
// disables the on-device wake word. enable=false sets the firmware's
// disable_voice, which makes it ignore trigger words entirely: no upload, no
// speech, and crucially no LED (the wake glow is drawn only when voice is
// enabled). This is how a muted Sense stops listening AND stops lighting up,
// which SET_VOLUME=0 alone cannot do (that only silences the speaker).
func VoiceControlBatch(enable bool, messageID int64) []byte {
	var vc []byte
	if enable {
		vc = appendField(vc, 1, wireVarint, 1) // false is the field's default; omit it
	}

	var msg []byte
	msg = appendField(msg, 2, wireVarint, 1) // order
	msg = appendField(msg, 3, wireVarint, uint64(messageID))
	msg = appendField(msg, 4, wireVarint, typeVoiceControl)
	msg = appendLenField(msg, 8, vc)

	return appendLenField(nil, 1, msg)
}

const (
	wireVarint = 0
	wireLen    = 2
)

func appendField(b []byte, field int, wire int, v uint64) []byte {
	b = appendVarint(b, uint64(field)<<3|uint64(wire))
	return appendVarint(b, v)
}

func appendLenField(b []byte, field int, payload []byte) []byte {
	b = appendVarint(b, uint64(field)<<3|wireLen)
	b = appendVarint(b, uint64(len(payload)))
	return append(b, payload...)
}

func appendVarint(b []byte, v uint64) []byte {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	return append(b, tmp[:n]...)
}

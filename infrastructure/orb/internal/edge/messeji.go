package edge

import "encoding/binary"

// Messeji is the server-to-device control channel (the /receive long-poll).
// The device applies commands it carries, notably the speaker volume, and
// validates the response's signature exactly like a sync response, so these
// bytes are handed to respondSigned, not written raw.
//
// The message the device decodes is a BatchMessage of Messages
// (proto/messeji). Rather than pull the whole messeji proto into orb, the one
// message shape we send, SET_VOLUME, is hand-encoded here: its field numbers
// are fixed by the proto and small.
//
//	BatchMessage { repeated Message message = 1 }
//	Message { int64 order = 2 (req); int64 message_id = 3;
//	          Type type = 4 (req, SET_VOLUME = 2); Volume volume = 7 }
//	Volume { uint32 volume = 1 (req) }   // 0-100 percent

const messejiTypeSetVolume = 2

// messejiVolumeBatch encodes a signed-ready BatchMessage carrying one
// SET_VOLUME command. volume is a percent (0-100); messageID lets the device
// acknowledge it on its next poll.
func messejiVolumeBatch(volume uint32, messageID int64) []byte {
	// Volume { volume = 1 }
	vol := appendField(nil, 1, wireVarint, uint64(volume))

	// Message { order=2, message_id=3, type=4, volume=7 }
	var msg []byte
	msg = appendField(msg, 2, wireVarint, 1)                       // order
	msg = appendField(msg, 3, wireVarint, uint64(messageID))       // message_id
	msg = appendField(msg, 4, wireVarint, messejiTypeSetVolume)    // type
	msg = appendLenField(msg, 7, vol)                              // volume submessage

	// BatchMessage { message = 1 }
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

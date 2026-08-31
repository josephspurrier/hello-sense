package speech

import "encoding/binary"

// IMA/DVI ADPCM decoder, matching the firmware coder in kitsune/adpcm.c (the
// classic CWI implementation). The device encodes one continuous stream from a
// zero predictor and zero index with no per-block header, and packs two
// samples per byte, the FIRST (even) sample in the high nibble and the second
// (odd) in the low nibble (bufferstep starts high). Decode in that same order.

var imaIndexTable = [16]int{
	-1, -1, -1, -1, 2, 4, 6, 8,
	-1, -1, -1, -1, 2, 4, 6, 8,
}

var imaStepTable = [89]int{
	7, 8, 9, 10, 11, 12, 13, 14, 16, 17,
	19, 21, 23, 25, 28, 31, 34, 37, 41, 45,
	50, 55, 60, 66, 73, 80, 88, 97, 107, 118,
	130, 143, 157, 173, 190, 209, 230, 253, 279, 307,
	337, 371, 408, 449, 494, 544, 598, 658, 724, 796,
	876, 963, 1060, 1166, 1282, 1411, 1552, 1707, 1878, 2066,
	2272, 2499, 2749, 3024, 3327, 3660, 4026, 4428, 4871, 5358,
	5894, 6484, 7132, 7845, 8630, 9493, 10442, 11487, 12635, 13899,
	15289, 16818, 18500, 20350, 22385, 24623, 27086, 29794, 32767,
}

// DecodeADPCM turns the device's ADPCM stream into 16-bit little-endian PCM,
// the format speech recognizers expect. Two output samples per input byte.
func DecodeADPCM(in []byte) []byte {
	out := make([]byte, 0, len(in)*4)
	valpred := 0
	index := 0
	step := imaStepTable[0]

	decode := func(delta int) {
		// The inverse of the coder's delta build: index walk, then the
		// vpdiff reconstruction from the step and the three magnitude bits.
		index += imaIndexTable[delta]
		if index < 0 {
			index = 0
		} else if index > 88 {
			index = 88
		}
		sign := delta & 8
		mag := delta & 7
		vpdiff := step >> 3
		if mag&4 != 0 {
			vpdiff += step
		}
		if mag&2 != 0 {
			vpdiff += step >> 1
		}
		if mag&1 != 0 {
			vpdiff += step >> 2
		}
		if sign != 0 {
			valpred -= vpdiff
		} else {
			valpred += vpdiff
		}
		if valpred > 32767 {
			valpred = 32767
		} else if valpred < -32768 {
			valpred = -32768
		}
		step = imaStepTable[index]

		var s [2]byte
		binary.LittleEndian.PutUint16(s[:], uint16(int16(valpred)))
		out = append(out, s[:]...)
	}

	for _, b := range in {
		decode(int(b>>4) & 0x0f) // even sample, high nibble first
		decode(int(b) & 0x0f)    // odd sample, low nibble
	}
	return out
}

// PCMToWAV wraps 16-bit mono little-endian PCM in a minimal RIFF/WAVE header,
// which is what most speech-to-text tools want on stdin or a file.
func PCMToWAV(pcm []byte, sampleRate int) []byte {
	var h [44]byte
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(pcm)))
	copy(h[8:], "WAVE")
	copy(h[12:], "fmt ")
	binary.LittleEndian.PutUint32(h[16:], 16) // PCM chunk size
	binary.LittleEndian.PutUint16(h[20:], 1)  // PCM
	binary.LittleEndian.PutUint16(h[22:], 1)  // mono
	binary.LittleEndian.PutUint32(h[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(h[28:], uint32(sampleRate*2)) // byte rate
	binary.LittleEndian.PutUint16(h[32:], 2)                    // block align
	binary.LittleEndian.PutUint16(h[34:], 16)                   // bits
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(pcm)))
	return append(h[:], pcm...)
}

// Package sense implements the Sense device's message framing: how a request
// body is signed, and how a response must be signed for the device to accept it.
//
// Ported from working-files/sense_server.py and cross-checked against
// SignedMessage.parse in suripu-core. The scheme is unusual enough that getting
// it slightly wrong produces "signature validation fail" on the device with no
// detail, so the layouts are spelled out here.
package sense

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
)

const (
	// IVLength is the AES block size, and the length of the IV that prefixes
	// every signature.
	IVLength = 16
	// SigLength is the encrypted signature: SHA1 of the body (20 bytes) padded
	// with zeros to 32, then AES-CBC encrypted.
	SigLength = 32
	// TrailerLength is what the device appends to a request body.
	TrailerLength = IVLength + SigLength // 48
)

var (
	ErrShortBody    = errors.New("sense: body too short to carry a signature")
	ErrBadKeyLen    = errors.New("sense: AES key must be 16 bytes")
	ErrBadSignature = errors.New("sense: signature does not match body")
)

// ParseSigned splits a request body from the device.
//
// Device to server layout is [protobuf][IV(16)][signature(32)], so the payload
// is everything except the trailing 48 bytes. Note this is the opposite order
// from the server's own responses, which put the IV and signature FIRST. That
// asymmetry is not a mistake in this code; it is what both ends do.
func ParseSigned(body []byte) (payload, iv, sig []byte, err error) {
	if len(body) < TrailerLength {
		return nil, nil, nil, fmt.Errorf("%w: got %d bytes, need at least %d",
			ErrShortBody, len(body), TrailerLength)
	}
	split := len(body) - TrailerLength
	payload = body[:split]
	iv = body[split : split+IVLength]
	sig = body[split+IVLength:]
	return payload, iv, sig, nil
}

// Verify checks a device-supplied signature against the payload.
//
// It DECRYPTS the signature and compares the first 20 bytes to SHA1(payload),
// rather than re-encrypting the hash and comparing ciphertext. Those are not
// equivalent, and the difference is why real devices were rejected while
// self-signed test data passed.
//
// The plaintext is a 20-byte SHA1 in a 32-byte block. suripu's validateWithKey
// compares exactly `for (int i = 0; i < 20; i++)`, leaving the trailing 12
// bytes unchecked, and the Sense does not zero them: it sends whatever is in
// that buffer. Encrypt-and-compare therefore requires 12 bytes to match that
// the device never promised, and fails against every genuine upload.
//
// Comparison is constant time over the 20 bytes that matter. The signature is
// not secret, but a timing oracle would let someone on the network forge a body
// a byte at a time, and constant time costs nothing.
func Verify(key, payload, iv, sig []byte) error {
	if len(key) != IVLength {
		return fmt.Errorf("%w: got %d", ErrBadKeyLen, len(key))
	}
	if len(sig) != SigLength {
		return fmt.Errorf("%w: signature is %d bytes, want %d", ErrBadSignature, len(sig), SigLength)
	}
	if len(iv) != IVLength {
		return fmt.Errorf("%w: iv is %d bytes, want %d", ErrBadSignature, len(iv), IVLength)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("sense: new cipher: %w", err)
	}
	plain := make([]byte, SigLength)
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, sig)

	sum := sha1.Sum(payload)
	if !hmac.Equal(plain[:len(sum)], sum[:]) {
		return ErrBadSignature
	}
	return nil
}

// Sign produces a response body the device will accept:
// [IV(16)][signature(32)][protobuf].
//
// The device reads a reply with a single recv() and only reads again if that
// first read filled its 2048-byte buffer, so a response must be written to the
// socket in one write. That is a caller concern, not handled here, but it is
// the reason responses are assembled into a single slice rather than streamed.
func Sign(key, payload []byte) ([]byte, error) {
	iv := make([]byte, IVLength)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return nil, fmt.Errorf("sense: read iv: %w", err)
	}
	sig, err := computeSignature(key, payload, iv)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, IVLength+SigLength+len(payload))
	out = append(out, iv...)
	out = append(out, sig...)
	out = append(out, payload...)
	return out, nil
}

// computeSignature is AES-CBC over SHA1(payload) zero-padded to 32 bytes.
//
// The padding to 32 is not PKCS#7 and not a security measure: SHA1 gives 20
// bytes, AES needs a multiple of 16, and the firmware pads with zeros to two
// blocks. Using a standard padding scheme here would break the device.
func computeSignature(key, payload, iv []byte) ([]byte, error) {
	if len(key) != IVLength {
		return nil, fmt.Errorf("%w: got %d", ErrBadKeyLen, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("sense: new cipher: %w", err)
	}
	sum := sha1.Sum(payload)
	padded := make([]byte, SigLength)
	copy(padded, sum[:])

	out := make([]byte, SigLength)
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return out, nil
}

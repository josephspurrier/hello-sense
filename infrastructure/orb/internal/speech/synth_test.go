package speech

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// slowReader yields its chunks one Read at a time, mimicking fragments
// arriving from the sidecar as they are synthesized.
type slowReader struct {
	chunks [][]byte
}

func (r *slowReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[0])
	if n < len(r.chunks[0]) {
		r.chunks[0] = r.chunks[0][n:]
	} else {
		r.chunks = r.chunks[1:]
	}
	return n, nil
}

func TestCopyPaddedPadsToTotal(t *testing.T) {
	src := &slowReader{chunks: [][]byte{[]byte("abcd"), []byte("efgh")}}
	var dst bytes.Buffer
	flushes := 0

	audio, truncated, err := CopyPadded(&dst, src, 100, func() { flushes++ })
	if err != nil {
		t.Fatal(err)
	}
	if audio != 8 {
		t.Fatalf("audio = %d, want 8", audio)
	}
	if truncated {
		t.Fatal("unexpected truncation")
	}
	if dst.Len() != 100 {
		t.Fatalf("wrote %d bytes, want exactly 100", dst.Len())
	}
	if !bytes.HasPrefix(dst.Bytes(), []byte("abcdefgh")) {
		t.Fatal("audio bytes not first")
	}
	if !bytes.Equal(dst.Bytes()[8:], SilenceMP3()[:92]) {
		t.Fatal("padding is not silence frames")
	}
	if flushes == 0 {
		t.Fatal("never flushed")
	}
}

func TestCopyPaddedTruncatesOverrun(t *testing.T) {
	src := strings.NewReader(strings.Repeat("x", 50))
	var dst bytes.Buffer

	audio, truncated, err := CopyPadded(&dst, src, 20, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Fatal("expected truncation")
	}
	if audio != 20 || dst.Len() != 20 {
		t.Fatalf("audio %d, wrote %d, want exactly 20", audio, dst.Len())
	}
}

// A source error mid-stream must still pad out to the declared length,
// because the device has already been promised Content-Length bytes.
type errAfterReader struct {
	data []byte
	done bool
}

func (r *errAfterReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, errors.New("sidecar died")
	}
	r.done = true
	return copy(p, r.data), nil
}

func TestCopyPaddedPadsThroughSourceError(t *testing.T) {
	src := &errAfterReader{data: []byte("audio")}
	var dst bytes.Buffer

	audio, _, err := CopyPadded(&dst, src, 40, nil)
	if err != nil {
		t.Fatal(err)
	}
	if audio != 5 {
		t.Fatalf("audio = %d, want 5", audio)
	}
	if dst.Len() != 40 {
		t.Fatalf("wrote %d bytes, want exactly 40", dst.Len())
	}
}

func TestCopyPaddedPadTotalLargerThanSilenceClip(t *testing.T) {
	total := len(SilenceMP3())*2 + 100
	var dst bytes.Buffer
	if _, _, err := CopyPadded(&dst, strings.NewReader(""), total, nil); err != nil {
		t.Fatal(err)
	}
	if dst.Len() != total {
		t.Fatalf("wrote %d bytes, want %d", dst.Len(), total)
	}
}

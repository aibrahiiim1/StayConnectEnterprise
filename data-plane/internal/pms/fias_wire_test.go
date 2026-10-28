package pms

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// Audit P7 M1: an STX followed by an unterminated stream ends with ErrFrameTooLong after at most MaxFrameBytes of
// body, instead of buffering until the read deadline.
func TestReadFramedRecord_UnterminatedFrameIsBounded(t *testing.T) {
	const total = 10 << 20
	src := io.MultiReader(bytes.NewReader([]byte{STX}), io.LimitReader(repeatByte('A'), total))
	cr := &countingReader{r: src}
	_, err := ReadFramedRecord(bufio.NewReader(cr))
	if !errors.Is(err, ErrFrameTooLong) {
		t.Fatalf("err = %v, want ErrFrameTooLong", err)
	}
	if cr.n > MaxFrameBytes+2*4096 {
		t.Fatalf("read %d bytes before refusing; want about MaxFrameBytes (%d)", cr.n, MaxFrameBytes)
	}
}

func TestReadFramedRecord_LimitAndNoise(t *testing.T) {
	atCap := strings.Repeat("x", MaxFrameBytes)
	var in bytes.Buffer
	in.WriteString("noise")
	_ = WriteFramedRecord(&in, "LA|")
	_ = WriteFramedRecord(&in, atCap)
	_ = WriteFramedRecord(&in, atCap+"y")
	br := bufio.NewReader(&in)
	if got, err := ReadFramedRecord(br); err != nil || got != "LA|" {
		t.Fatalf("first = %q, %v", got, err)
	}
	if got, err := ReadFramedRecord(br); err != nil || got != atCap {
		t.Fatalf("frame at the cap: len=%d err=%v", len(got), err)
	}
	if _, err := ReadFramedRecord(br); !errors.Is(err, ErrFrameTooLong) {
		t.Fatalf("frame over the cap: err = %v, want ErrFrameTooLong", err)
	}
}

func TestReadFramedRecord_EOFMidFrame(t *testing.T) {
	br := bufio.NewReader(strings.NewReader("\x02GI|RN1"))
	if _, err := ReadFramedRecord(br); !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want EOF", err)
	}
}

type repeatByte byte

func (b repeatByte) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

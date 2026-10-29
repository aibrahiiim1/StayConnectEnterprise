// Package pms holds the FIAS wire primitives pmsd uses: record framing (STX ... ETX), the bounded frame reader and room
// normalisation. The legacy in-process provider layer that used to live here (the Provider interface, the registry, the
// ProtelFIAS / Mews / Apaleo / Stub providers and their scd loader and guard) was retired (audit AD-39); the live runtime
// is pmsd (internal/pmsd) with connectors in internal/pmsprovider. New connectors belong there.
package pms

import (
	"bufio"
	"errors"
	"strings"
)

const (
	stx = 0x02
	etx = 0x03
)

// MaxFrameBytes bounds one FIAS frame body (STX..ETX exclusive). Real records are a few hundred bytes at most; the cap
// only stops an unterminated frame from growing memory at line rate until the read deadline (audit P7 M1).
const MaxFrameBytes = 16 << 10

// ErrFrameTooLong is returned when a frame body exceeds MaxFrameBytes without an ETX; the stream is no longer framed
// and the caller must end the link.
var ErrFrameTooLong = errors.New("fias: frame exceeds maximum length")

// Bytes outside the frame are ignored (the stream may carry handshake noise).
func readFramedRecord(br *bufio.Reader) (string, error) {
	for {
		b, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		if b != stx {
			continue
		}
		var body []byte
		for {
			chunk, err := br.ReadSlice(etx)
			if len(body)+len(chunk) > MaxFrameBytes+1 { // +1: the ETX itself
				return "", ErrFrameTooLong
			}
			body = append(body, chunk...)
			if err == bufio.ErrBufferFull {
				continue
			}
			if err != nil {
				return "", err
			}
			break
		}
		if n := len(body); n > 0 && body[n-1] == etx {
			body = body[:n-1]
		}
		return string(body), nil
	}
}

// NormalizeRoom is the room key pmsd compares rooms by.
func NormalizeRoom(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

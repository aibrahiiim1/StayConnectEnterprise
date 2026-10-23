// Package postinghandoff is the local unix-socket hand-off from the posting engine to pmsd, which owns the
// property's single FIAS connection. pmsd only carries the exact bytes or refuses them; money decisions
// stay in the posting engine and the database.
package postinghandoff

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"time"
)

// DefaultSocket lives in pmsd's own runtime directory, the only place its sandboxed unit (ProtectSystem=strict)
// can create a socket. The socket is 0600 and owned by pmsd; only root (scd) can also connect.
const DefaultSocket = "/run/stayconnect-pmsd/pmsd-posting.sock"

// EnvSocket overrides DefaultSocket on both sides.
const EnvSocket = "STAYCONNECT_POSTING_HANDOFF_SOCKET"

const ProtocolVersion = 1

// MaxBody bounds a PS record body; a FIAS PS is a few dozen bytes.
const MaxBody = 512

// MaxWait bounds how long pmsd may wait for the PA.
const MaxWait = 60 * time.Second

// Request is one immutable posting command: the exact recorded PS body and its SHA-256.
type Request struct {
	Version     int    `json:"v"`
	InterfaceID string `json:"interface_id"`
	PNumber     int64  `json:"p_number"`
	Body        string `json:"body"`
	BodySHA256  string `json:"body_sha256"`
	WaitMillis  int64  `json:"wait_ms"`
}

// Result kinds map one-to-one onto the posting engine's three-valued UNKNOWN contract.
const (
	// Answered: a PA with the command's P# was read on the same link; PABody is that record.
	Answered = "ANSWERED"
	// NotTransmitted: pmsd provably wrote no byte of this command.
	NotTransmitted = "NOT_TRANSMITTED"
	// Unknown: the command may have reached the PMS and no matching PA was read in time. Never retried.
	Unknown = "UNKNOWN"
)

type Response struct {
	Result string `json:"result"`
	// Code is a bounded reason, never guest data (e.g. LINK_NOT_READY, ANSWER_TIMEOUT).
	Code   string `json:"code,omitempty"`
	PABody string `json:"pa_body,omitempty"`
}

// BodyHash is the command's identity: lowercase hex SHA-256 of the PS body.
func BodyHash(body string) string {
	s := sha256.Sum256([]byte(body))
	return hex.EncodeToString(s[:])
}

// Validate checks shape only (version, bounded wire-safe body, hash, P#); pmsd separately asks the
// database whether the command is authorised.
func (r Request) Validate() error {
	if r.Version != ProtocolVersion {
		return errors.New("unsupported hand-off version")
	}
	if strings.TrimSpace(r.InterfaceID) == "" || len(r.InterfaceID) > 64 {
		return errors.New("interface id missing")
	}
	if r.PNumber <= 0 {
		return errors.New("protocol-attempt reference missing")
	}
	if len(r.Body) < 4 || len(r.Body) > MaxBody || !strings.HasPrefix(r.Body, "PS|") || !strings.HasSuffix(r.Body, "|") {
		return errors.New("not a PS record")
	}
	for i := 0; i < len(r.Body); i++ {
		if c := r.Body[i]; c < 0x20 || c == 0x7f {
			return errors.New("control byte in the record")
		}
	}
	if r.BodySHA256 != BodyHash(r.Body) {
		return errors.New("the body does not match its hash")
	}
	if PNumberOf(r.Body) != r.PNumber {
		return errors.New("the body does not carry the command's protocol-attempt reference")
	}
	return nil
}

// PNumberOf returns the P# a PS or PA record carries: exactly one P# field of digits, else 0.
func PNumberOf(body string) int64 {
	var n int64
	found := 0
	for _, tok := range strings.Split(body, "|")[1:] {
		if len(tok) < 2 || tok[:2] != "P#" {
			continue
		}
		v := tok[2:]
		if v == "" || len(v) > 18 {
			return 0
		}
		var x int64
		for k := 0; k < len(v); k++ {
			if v[k] < '0' || v[k] > '9' {
				return 0
			}
			x = x*10 + int64(v[k]-'0')
		}
		found++
		n = x
	}
	if found != 1 {
		return 0
	}
	return n
}

// ErrNotSent means the socket could not be reached, so no command was delivered.
var ErrNotSent = errors.New("posting hand-off: pmsd could not be reached; nothing was handed off")

// Send hands one command to pmsd. Only a failed dial is reported as not-sent; once connected, anything
// short of a complete Response is an error the caller must treat as UNKNOWN (pmsd may have written the PS).
func Send(ctx context.Context, socket string, req Request) (Response, error) {
	if socket == "" {
		socket = DefaultSocket
	}
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return Response{}, ErrNotSent
	}
	defer conn.Close()
	wait := time.Duration(req.WaitMillis) * time.Millisecond
	if wait <= 0 || wait > MaxWait {
		wait = MaxWait
	}
	_ = conn.SetDeadline(time.Now().Add(wait + 10*time.Second))
	b, _ := json.Marshal(req)
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return Response{}, errors.New("posting hand-off: the command may have been delivered; the answer is unknown")
	}
	line, err := bufio.NewReaderSize(conn, 4096).ReadBytes('\n')
	if err != nil {
		return Response{}, errors.New("posting hand-off: no answer from pmsd; the outcome is unknown")
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return Response{}, errors.New("posting hand-off: unreadable answer from pmsd; the outcome is unknown")
	}
	switch resp.Result {
	case Answered, NotTransmitted, Unknown:
		return resp, nil
	}
	return Response{}, errors.New("posting hand-off: unrecognised answer from pmsd; the outcome is unknown")
}

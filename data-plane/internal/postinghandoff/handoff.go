// Package postinghandoff is the narrow local hand-off between the financial execution path and pmsd.
//
// THE ARCHITECTURE (Product-Owner decision D45, docs/architecture/ONEGATE_MODULES_AND_ACQUISITION.md §11):
// the property's PMS accepts ONE FIAS client connection, and pmsd owns it. A room charge therefore travels
//
//	financial validation (scd, internal/posting)  ->  immutable authorised posting command
//	  ->  this hand-off (a root-only unix socket on the appliance)  ->  pmsd writes the PS on its FIAS link
//	  ->  the PMS answers with a PA  ->  pmsd returns the matched PA body here, verbatim
//	  ->  the financial path records the outcome and settles only on PA=OK.
//
// pmsd is TRANSPORT. It carries the exact bytes it is handed or it refuses them; it never builds, edits,
// re-targets, retries or interprets a charge. Everything that decides money lives on the other side of this
// socket, in the posting engine and the database.
//
// This package holds only the wire contract and the client. It imports neither the posting core nor pmsd,
// dials only a local unix socket (never TCP), and makes no financial decision: it reports what pmsd said.
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

// DefaultSocket is where pmsd listens: its own runtime directory (RuntimeDirectory=stayconnect-pmsd, 0750, owned
// by the pmsd service user) -- the only place the shipped unit (User=stayconnect-pmsd, ProtectSystem=strict) can
// create a socket. The socket is 0600 and owned by pmsd; scd, which runs as root, is the only other process that
// can connect. (The former default, /run/stayconnect, is root-owned and read-only to pmsd, so an installed
// appliance could never open the posting relay.)
const DefaultSocket = "/run/stayconnect-pmsd/pmsd-posting.sock"

// EnvSocket overrides DefaultSocket on BOTH sides (tests and non-default layouts).
const EnvSocket = "STAYCONNECT_POSTING_HANDOFF_SOCKET"

// ProtocolVersion is carried on every request; pmsd refuses any other.
const ProtocolVersion = 1

// MaxBody bounds a PS record body. A FIAS PS is a few dozen bytes; anything near this is not one.
const MaxBody = 512

// MaxWait bounds how long pmsd may wait for the PA. It is a bound, not a retry timer.
const MaxWait = 60 * time.Second

// Request is one immutable posting command: the exact PS body the financial path built and durably recorded,
// its SHA-256, the interface whose link must carry it, and the protocol-attempt reference it owns.
type Request struct {
	Version     int    `json:"v"`
	InterfaceID string `json:"interface_id"`
	PNumber     int64  `json:"p_number"`
	Body        string `json:"body"`
	BodySHA256  string `json:"body_sha256"`
	WaitMillis  int64  `json:"wait_ms"`
}

// Result kinds. They map ONE-TO-ONE onto the posting engine's three-valued UNKNOWN contract.
const (
	// Answered: a PA whose P# equals the command's was read on the same link. PABody is that record, verbatim.
	Answered = "ANSWERED"
	// NotTransmitted: pmsd PROVABLY wrote no byte of this command (refused before the writer took it).
	NotTransmitted = "NOT_TRANSMITTED"
	// Unknown: the command may have reached the PMS and no matching PA was read in time. Never retried.
	Unknown = "UNKNOWN"
)

// Response is pmsd's single answer to a Request.
type Response struct {
	Result string `json:"result"`
	// Code is a bounded reason (never guest data), e.g. LINK_NOT_READY, COMMAND_NOT_AUTHORISED, ANSWER_TIMEOUT.
	Code   string `json:"code,omitempty"`
	PABody string `json:"pa_body,omitempty"`
}

// BodyHash is the command's identity: lowercase hex SHA-256 of the exact PS body bytes.
func BodyHash(body string) string {
	s := sha256.Sum256([]byte(body))
	return hex.EncodeToString(s[:])
}

// Validate checks a request's SHAPE: version, bounded wire-safe PS body, hash, P# carried by the body. It
// decides nothing financial; pmsd additionally asks the database whether the command is authorised.
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

// ErrNotSent means the client PROVABLY delivered no command to pmsd (the socket could not be reached).
var ErrNotSent = errors.New("posting hand-off: pmsd could not be reached; nothing was handed off")

// Send hands one command to pmsd and returns pmsd's answer.
//
// The ONLY outcome reported as not-sent without pmsd saying so is a failed dial: no connection means no
// command. Once a connection exists, anything short of a complete, well-formed Response is returned as an
// error the caller MUST treat as UNKNOWN -- pmsd may already have written the PS.
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

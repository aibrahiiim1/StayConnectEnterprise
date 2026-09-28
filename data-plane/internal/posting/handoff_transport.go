package posting

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/postinghandoff"
)

// handoffTransport is the production financial transport (decision D45): it hands the immutable posting
// command to pmsd, which owns the property's single FIAS connection, and turns pmsd's answer back into the
// engine's three-valued UNKNOWN contract.
//
// It lives behind DarkGuard like any transport, it has no socket of its own (postinghandoff owns the local
// unix dial), and it adds no judgement: the mapping below is exhaustive and conservative. Only two things
// ever count as "not transmitted" -- pmsd could not be reached at all, or pmsd itself said it wrote nothing.
// Everything else that is not a matched PA is UNKNOWN, and UNKNOWN is never retried by anyone.
type handoffTransport struct {
	socket string
	send   func(ctx context.Context, socket string, req postinghandoff.Request) (postinghandoff.Response, error)
}

func newHandoffTransport() *handoffTransport {
	return &handoffTransport{socket: strings.TrimSpace(os.Getenv(postinghandoff.EnvSocket)), send: postinghandoff.Send}
}

func (h *handoffTransport) SendPS(ctx context.Context, interfaceID string, pNumber int64, body string) (*PA, error) {
	req := postinghandoff.Request{
		Version: postinghandoff.ProtocolVersion, InterfaceID: interfaceID, PNumber: pNumber,
		Body: body, BodySHA256: postinghandoff.BodyHash(body), WaitMillis: AnswerDeadline.Milliseconds(),
	}
	if err := req.Validate(); err != nil {
		// Refused before anything left this process: provably not transmitted.
		return nil, &handoffError{code: ErrWireFieldInvalid, msg: "the posting command is malformed: " + err.Error(), notSent: true}
	}
	cctx, cancel := context.WithTimeout(ctx, AnswerDeadline+15*time.Second)
	defer cancel()
	resp, err := h.send(cctx, h.socket, req)
	if err == postinghandoff.ErrNotSent {
		return nil, &handoffError{code: ErrTransportUnavailable, msg: "pmsd could not be reached; nothing was handed off", notSent: true}
	}
	if err != nil {
		return nil, &handoffError{code: ErrTransportUnavailable, msg: err.Error()} // UNKNOWN
	}
	switch resp.Result {
	case postinghandoff.NotTransmitted:
		return nil, &handoffError{code: ErrTransportUnavailable, msg: "pmsd wrote nothing: " + boundedCode(resp.Code), notSent: true}
	case postinghandoff.Answered:
		pa, perr := ParsePA(resp.PABody)
		if perr != nil {
			// A PA was read but cannot be understood: the PS reached the PMS and its effect is unknown.
			return nil, &handoffError{code: CodeOf(perr), msg: "the PMS answer could not be interpreted"}
		}
		if pa.PNumber != pNumber {
			return nil, &handoffError{code: ErrPAWrongPNumber, msg: "the PMS answered a different protocol-attempt reference"}
		}
		return &pa, nil
	default: // Unknown
		return nil, &handoffError{code: ErrTransportUnavailable, msg: "no conclusive answer: " + boundedCode(resp.Code)}
	}
}

// boundedCode keeps pmsd's reason to a short protocol word, so nothing unexpected reaches an attempt event.
func boundedCode(c string) string {
	if len(c) > 48 {
		c = c[:48]
	}
	for i := 0; i < len(c); i++ {
		ch := c[i]
		if !(ch == '_' || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')) {
			return "UNSPECIFIED"
		}
	}
	if c == "" {
		return "UNSPECIFIED"
	}
	return c
}

// handoffError is a typed posting error that is ALSO ErrNotTransmitted when, and only when, nothing was sent,
// and ErrTransmittedNoAnswer otherwise -- so the engine's NotTransmitted() test is the whole classification.
type handoffError struct {
	code    Code
	msg     string
	notSent bool
}

func (e *handoffError) Error() string { return e.msg }
func (e *handoffError) Unwrap() []error {
	if e.notSent {
		return []error{&Error{Code: e.code, Msg: e.msg}, ErrNotTransmitted}
	}
	return []error{&Error{Code: e.code, Msg: e.msg}, ErrTransmittedNoAnswer}
}

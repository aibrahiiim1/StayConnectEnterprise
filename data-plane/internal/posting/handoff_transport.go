package posting

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/postinghandoff"
)

// handoffTransport hands the posting command to pmsd, which owns the FIAS connection. Only two outcomes
// count as "not transmitted" (pmsd unreachable, or pmsd said it wrote nothing); anything else that is not
// a matched PA is UNKNOWN, and UNKNOWN is never retried.
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
		return nil, &handoffError{code: ErrTransportUnavailable, msg: "pmsd wrote nothing: " + boundedCode(resp.Code), notSent: true,
			reason: boundedCode(resp.Code)}
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

// boundedCode keeps pmsd's reason to a short protocol word so nothing unexpected reaches an attempt event.
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

// handoffError is ErrNotTransmitted only when nothing was sent, ErrTransmittedNoAnswer otherwise.
type handoffError struct {
	code    Code
	msg     string
	notSent bool
	reason  string // pmsd's bounded reason when it proved nothing was written (e.g. ROOM_CHANGED)
}

func (e *handoffError) Error() string { return e.msg }

func (e *handoffError) NotSentReason() string { return e.reason }
func (e *handoffError) Unwrap() []error {
	if e.notSent {
		return []error{&Error{Code: e.code, Msg: e.msg}, ErrNotTransmitted}
	}
	return []error{&Error{Code: e.code, Msg: e.msg}, ErrTransmittedNoAnswer}
}

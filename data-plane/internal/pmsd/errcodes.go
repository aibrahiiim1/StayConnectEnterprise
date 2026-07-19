package pmsd

import (
	"context"
	"errors"
	"log/slog"
)

// Code is the CLOSED, bounded vocabulary of machine error codes that pmsd may persist to
// iam_v2.pms_interface_runtime, emit as a metric label, or write to structured logs. Nothing derived from
// an arbitrary err.Error() string is ever persisted or exported — only a Code from this set. This prevents
// a PMS endpoint, guest field, room number, reservation number, Folio number, secret, or stack trace from
// leaking into durable state or telemetry.
type Code string

const (
	CodeNone                Code = ""
	CodeDBUnavailable       Code = "DB_UNAVAILABLE"
	CodeLockNotAcquired     Code = "LOCK_NOT_ACQUIRED"
	CodeLockSessionLost     Code = "LOCK_SESSION_LOST"
	CodeInterfaceDisabled   Code = "INTERFACE_DISABLED"
	CodeAssignmentChanged   Code = "ASSIGNMENT_SCOPE_CHANGED"
	CodeRevisionInvalid     Code = "REVISION_INVALID"
	CodeRevisionChanged     Code = "REVISION_CHANGED"
	CodeConfigInvalid       Code = "CONFIG_INVALID"
	CodeSecretMissing       Code = "SECRET_GENERATION_MISSING"
	CodeSecretRotated       Code = "SECRET_GENERATION_ROTATED"
	CodeSecretDecryptFailed Code = "SECRET_DECRYPT_FAILED"
	CodeDialTimeout         Code = "DIAL_TIMEOUT"
	CodeDialFailed          Code = "DIAL_FAILED"
	CodeProtocolFraming     Code = "PROTOCOL_FRAMING_ERROR"
	CodeProtocolLinkEnded   Code = "PROTOCOL_LINK_ENDED"
	CodeRuntimeGenStale     Code = "RUNTIME_GENERATION_STALE"
	CodeQueueOverflow       Code = "QUEUE_OVERFLOW"
	CodeContextCanceled     Code = "CONTEXT_CANCELED"
	CodePanicRecovered      Code = "PANIC_RECOVERED"
	CodeOutboundBlocked     Code = "OUTBOUND_FRAME_BLOCKED"
	CodeUnclassified        Code = "UNCLASSIFIED" // never carries the raw text
)

// codeSet is the authoritative allowlist; Valid() and tests assert nothing outside it is ever produced.
var codeSet = map[Code]struct{}{
	CodeNone: {}, CodeDBUnavailable: {}, CodeLockNotAcquired: {}, CodeLockSessionLost: {},
	CodeInterfaceDisabled: {}, CodeAssignmentChanged: {}, CodeRevisionInvalid: {}, CodeRevisionChanged: {},
	CodeConfigInvalid: {}, CodeSecretMissing: {}, CodeSecretRotated: {}, CodeSecretDecryptFailed: {},
	CodeDialTimeout: {}, CodeDialFailed: {}, CodeProtocolFraming: {}, CodeProtocolLinkEnded: {},
	CodeRuntimeGenStale: {}, CodeQueueOverflow: {}, CodeContextCanceled: {}, CodePanicRecovered: {},
	CodeOutboundBlocked: {}, CodeUnclassified: {},
}

func (c Code) Valid() bool { _, ok := codeSet[c]; return ok }
func (c Code) String() string {
	if !c.Valid() {
		return string(CodeUnclassified)
	}
	return string(c)
}

// typedErr binds a Code to an underlying error for local diagnostics only. The underlying error is NEVER
// persisted or exported — only Code() is.
type typedErr struct {
	code Code
	err  error
}

func (e *typedErr) Error() string { return e.code.String() } // deliberately no raw detail
func (e *typedErr) Unwrap() error { return e.err }
func (e *typedErr) Code() Code    { return e.code }

// coded wraps err with a machine Code. If err already carries a Code it is preserved.
func coded(code Code, err error) error {
	if err == nil {
		return &typedErr{code: code}
	}
	return &typedErr{code: code, err: err}
}

// Classify returns the bounded Code for an error. It inspects typed wrappers and structural conditions
// only; it NEVER inspects or returns err.Error() text.
func Classify(err error) Code {
	if err == nil {
		return CodeNone
	}
	var te *typedErr
	if errors.As(err, &te) {
		return te.code
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return CodeContextCanceled
	case errors.Is(err, ErrStaleGeneration):
		return CodeRuntimeGenStale
	case errors.Is(err, ErrMalformedUUID):
		return CodeConfigInvalid
	default:
		return CodeUnclassified
	}
}

// Stage is a bounded lifecycle-stage label (closed set) safe for logs/metrics. It describes WHERE in the
// ownership cycle an event happened, never WHAT data was involved.
type Stage string

const (
	StageDiscover  Stage = "DISCOVER"
	StageLock      Stage = "LOCK"
	StageReRead    Stage = "REREAD"
	StageAllocate  Stage = "ALLOCATE_GENERATION"
	StageSecret    Stage = "SECRET"
	StageDial      Stage = "DIAL"
	StageServe     Stage = "SERVE"
	StagePersist   Stage = "PERSIST"
	StageReconnect Stage = "RECONNECT"
	StageShutdown  Stage = "SHUTDOWN"
)

// SafeFields are the ONLY values allowed into a pmsd structured log line. Every field is a bounded machine
// value — a UUID, a monotonic counter, a closed-set stage label — NEVER PMS/guest/secret-derived text. The
// raw error is deliberately absent: it stays in memory for control flow (Classify inspects its type only)
// and is never rendered, redacted, or serialized. There is intentionally no regex "redaction" acting as a
// security boundary; safety comes from the fact that no unsafe field can enter here at all.
type SafeFields struct {
	InterfaceID string // canonical UUID only
	Generation  int64
	Stage       Stage
	Attempt     int
}

// logCoded emits the bounded Code plus SafeFields. It NEVER receives or logs a raw error, redacted or not.
func logCoded(log *slog.Logger, msg string, code Code, sf SafeFields) {
	if log == nil {
		return
	}
	log.Error(msg,
		"code", code.String(),
		"interface", sf.InterfaceID,
		"generation", sf.Generation,
		"stage", string(sf.Stage),
		"attempt", sf.Attempt,
	)
}

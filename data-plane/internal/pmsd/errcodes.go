package pmsd

import (
	"context"
	"errors"
	"log/slog"
)

// Code is the closed vocabulary of error codes pmsd may persist, label or log. Nothing derived from err.Error()
// is ever exported, so no PMS endpoint, guest field, room, reservation, folio or secret can leak.
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
	CodeEventInvalid        Code = "EVENT_INVALID"
	CodeAssignmentMissing   Code = "ASSIGNMENT_MISSING"
	CodeContextCanceled     Code = "CONTEXT_CANCELED"
	CodePanicRecovered      Code = "PANIC_RECOVERED"
	CodeOutboundBlocked     Code = "OUTBOUND_FRAME_BLOCKED"
	CodeUnclassified        Code = "UNCLASSIFIED" // never carries the raw text

	CodeProviderAuth        Code = "PROVIDER_AUTH_FAILED"      // 401/403 or a refused token request
	CodeProviderRateLimited Code = "PROVIDER_RATE_LIMITED"     // 429 persisted past the bounded retries
	CodeProviderTimeout     Code = "PROVIDER_TIMEOUT"          // request timeout / 408 / 504 after retries
	CodeProviderUnavailable Code = "PROVIDER_UNAVAILABLE"      // 5xx or network failure after retries
	CodeProviderResponse    Code = "PROVIDER_RESPONSE_INVALID" // malformed or unexpected response body
	CodeProviderRejected    Code = "PROVIDER_REQUEST_REJECTED" // any other 4xx
)

var codeSet = map[Code]struct{}{
	CodeNone: {}, CodeDBUnavailable: {}, CodeLockNotAcquired: {}, CodeLockSessionLost: {},
	CodeInterfaceDisabled: {}, CodeAssignmentChanged: {}, CodeRevisionInvalid: {}, CodeRevisionChanged: {},
	CodeConfigInvalid: {}, CodeSecretMissing: {}, CodeSecretRotated: {}, CodeSecretDecryptFailed: {},
	CodeDialTimeout: {}, CodeDialFailed: {}, CodeProtocolFraming: {}, CodeProtocolLinkEnded: {},
	CodeRuntimeGenStale: {}, CodeQueueOverflow: {}, CodeEventInvalid: {}, CodeAssignmentMissing: {},
	CodeContextCanceled: {}, CodePanicRecovered: {}, CodeOutboundBlocked: {}, CodeUnclassified: {},
	CodeProviderAuth: {}, CodeProviderRateLimited: {}, CodeProviderTimeout: {}, CodeProviderUnavailable: {},
	CodeProviderResponse: {}, CodeProviderRejected: {},
}

func (c Code) Valid() bool { _, ok := codeSet[c]; return ok }
func (c Code) String() string {
	if !c.Valid() {
		return string(CodeUnclassified)
	}
	return string(c)
}

// typedErr binds a Code to an underlying error for local diagnostics; only Code() is ever persisted or exported.
type typedErr struct {
	code Code
	err  error
}

func (e *typedErr) Error() string { return e.code.String() } // deliberately no raw detail
func (e *typedErr) Unwrap() error { return e.err }
func (e *typedErr) Code() Code    { return e.code }

func coded(code Code, err error) error {
	if err == nil {
		return &typedErr{code: code}
	}
	return &typedErr{code: code, err: err}
}

// Classify returns the bounded Code for an error from its type only, never from err.Error() text.
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
	case errors.Is(err, ErrNoAssignment), errors.Is(err, ErrAssignmentNotGranting):
		return CodeAssignmentMissing
	case errors.Is(err, ErrIdentityUnreadable):
		return CodeConfigInvalid
	default:
		return CodeUnclassified
	}
}

// Stage is a closed-set lifecycle label: where in the ownership cycle, never what data.
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

var stageSet = map[Stage]struct{}{
	StageDiscover: {}, StageLock: {}, StageReRead: {}, StageAllocate: {}, StageSecret: {},
	StageDial: {}, StageServe: {}, StagePersist: {}, StageReconnect: {}, StageShutdown: {},
}

func (s Stage) Valid() bool { _, ok := stageSet[s]; return ok }
func (s Stage) safe() string {
	if !s.Valid() {
		return "INVALID_STAGE"
	}
	return string(s)
}

// LogEvent is the closed vocabulary of log-line names; free-text messages never reach the logs.
type LogEvent string

const (
	EventWorkerLockFailed       LogEvent = "WORKER_LOCK_FAILED"
	EventWorkerNotDialable      LogEvent = "WORKER_NOT_DIALABLE"
	EventWorkerRevisionInvalid  LogEvent = "WORKER_REVISION_INVALID"
	EventWorkerSecretMissing    LogEvent = "WORKER_SECRET_MISSING"
	EventWorkerSecretDecrypt    LogEvent = "WORKER_SECRET_DECRYPT_FAILED"
	EventWorkerDialFailed       LogEvent = "WORKER_DIAL_FAILED"
	EventWorkerLockLost         LogEvent = "WORKER_LOCK_LOST"
	EventWorkerGenerationStale  LogEvent = "WORKER_GENERATION_STALE"
	EventWorkerProtocolEnded    LogEvent = "WORKER_PROTOCOL_ENDED"
	EventWorkerQueueOverflow    LogEvent = "WORKER_QUEUE_OVERFLOW"
	EventWorkerPersistFailed    LogEvent = "WORKER_PERSIST_FAILED"
	EventWorkerPanicRecovered   LogEvent = "WORKER_PANIC_RECOVERED"
	EventWorkerReconnect        LogEvent = "WORKER_RECONNECT"
	EventSupervisorReconcileErr LogEvent = "SUPERVISOR_RECONCILE_FAILED"
	EventSupervisorAssignChange LogEvent = "SUPERVISOR_ASSIGNMENT_CHANGED"
	EventSupervisorNoAssignment LogEvent = "SUPERVISOR_NO_ASSIGNMENT"

	// Success-path events, so a recovered feed is distinguishable from a dead one in the journal.
	EventWorkerConnected       LogEvent = "WORKER_CONNECTED"
	EventWorkerResyncRequested LogEvent = "WORKER_RESYNC_REQUESTED"
	EventWorkerResyncPublished LogEvent = "WORKER_RESYNC_PUBLISHED"
)

var logEventSet = map[LogEvent]struct{}{
	EventWorkerLockFailed: {}, EventWorkerNotDialable: {}, EventWorkerRevisionInvalid: {},
	EventWorkerSecretMissing: {}, EventWorkerSecretDecrypt: {}, EventWorkerDialFailed: {},
	EventWorkerLockLost: {}, EventWorkerGenerationStale: {}, EventWorkerProtocolEnded: {},
	EventWorkerQueueOverflow: {}, EventWorkerPersistFailed: {}, EventWorkerPanicRecovered: {},
	EventWorkerReconnect: {}, EventSupervisorReconcileErr: {}, EventSupervisorAssignChange: {},
	EventSupervisorNoAssignment: {},
	EventWorkerConnected:        {}, EventWorkerResyncRequested: {}, EventWorkerResyncPublished: {},
}

func (e LogEvent) Valid() bool { _, ok := logEventSet[e]; return ok }
func (e LogEvent) safe() string {
	if !e.Valid() {
		return "INVALID_LOG_EVENT"
	}
	return string(e)
}

// UUIDValue is a validated identity for logging; an unparseable one renders as a fixed placeholder.
type UUIDValue struct {
	s     string
	valid bool
}

func NewUUIDValue(s string) UUIDValue {
	_, err := parseUUID16(s)
	return UUIDValue{s: s, valid: err == nil}
}
func (u UUIDValue) String() string {
	if !u.valid {
		return "INVALID_UUID"
	}
	return u.s
}

// SafeFields are the only values allowed into a pmsd log line: bounded machine values, never PMS, guest or
// secret-derived text. The raw error is deliberately absent and never rendered.
type SafeFields struct {
	InterfaceID UUIDValue
	Generation  int64
	Stage       Stage
	Attempt     int
	// Records is a count of domain records in a completed sync, rendered by progress lines only.
	Records int64
}

// logEvent emits a closed LogEvent, Code and SafeFields; it takes no free text or raw error. A nil logger is a no-op.
func logEvent(log *slog.Logger, event LogEvent, code Code, sf SafeFields) {
	if log == nil {
		return
	}
	log.Error(event.safe(),
		"code", code.String(),
		"interface", sf.InterfaceID.String(),
		"generation", sf.Generation,
		"stage", sf.Stage.safe(),
		"attempt", sf.Attempt,
	)
}

// logProgress is logEvent at INFO for successes, so operators filtering on severity still see a reconnect.
func logProgress(log *slog.Logger, event LogEvent, sf SafeFields) {
	if log == nil {
		return
	}
	log.Info(event.safe(),
		"code", CodeNone.String(),
		"interface", sf.InterfaceID.String(),
		"generation", sf.Generation,
		"stage", sf.Stage.safe(),
		"records", sf.Records,
	)
}

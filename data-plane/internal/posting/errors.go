package posting

import "errors"

// Code is a non-sensitive classification of why a posting was not executed; it never carries an amount, guest
// identifier, room number or secret.
type Code string

const (
	ErrConfig Code = "config"
	ErrRepo   Code = "repository"

	// Fail-closed creation gate; all happen before any side effect.
	ErrPostingTargetUnset  Code = "posting_target_unset"
	ErrReservationMismatch Code = "reservation_mismatch"
	ErrStayChargeOpen      Code = "stay_room_charge_unresolved"
	ErrRNMissing           Code = "rn_missing"
	ErrGNumberMissing      Code = "g_number_missing"
	ErrRNNotWireSafe       Code = "rn_not_wire_safe"
	ErrGNumberNotWireSafe  Code = "g_number_not_wire_safe"
	ErrStayNotInHouse      Code = "stay_not_in_house"
	ErrPostingNotAllowed   Code = "posting_not_allowed"
	ErrEvidenceStale       Code = "evidence_stale"
	ErrEvidenceOutOfScope  Code = "evidence_out_of_scope"
	ErrInterfaceNoCurrency Code = "interface_currency_not_onboarded"
	ErrCurrencyMismatch    Code = "currency_mismatch"
	ErrExponentMismatch    Code = "currency_exponent_mismatch"
	ErrAmountInvalid       Code = "amount_invalid"
	ErrInterfaceInactive   Code = "interface_not_accepting_work"
	ErrInterfaceDecomm     Code = "interface_decommissioned"
	ErrInterfaceNotFresh   Code = "interface_not_fresh"
	ErrFIASExponent        Code = "fias_exponent_unsupported"
	ErrReversalDisabled    Code = "programmatic_reversal_disabled"

	ErrDarkNoEgress     Code = "dark_no_egress"
	ErrAlreadyInFlight  Code = "already_in_flight"
	ErrRetryNotAuthed   Code = "retry_not_authorized"
	ErrPACorrelation    Code = "pa_correlation_failed"
	ErrPAStatusUnknown  Code = "pa_status_not_in_catalog"
	ErrPAAmbiguous      Code = "pa_ambiguous"
	ErrPAWrongPNumber   Code = "pa_wrong_p_number"
	ErrLaneBusy         Code = "interface_lane_busy"
	ErrUnknownTerminal  Code = "unknown_requires_manual_review"
	ErrReviewConflict   Code = "review_conflict"
	ErrReviewStale      Code = "review_version_stale"
	ErrWireFieldInvalid Code = "wire_field_invalid"
	// ErrTransportUnavailable: the hand-off to pmsd gave no matched PA; "not sent" vs UNKNOWN is carried by ErrNotTransmitted / ErrTransmittedNoAnswer, never by this code.
	ErrTransportUnavailable Code = "transport_unavailable"
)

// Msg must never contain secrets, card data, guest PII or amounts.
type Error struct {
	Code Code
	Msg  string
}

func (e *Error) Error() string {
	if e.Msg == "" {
		return "posting: " + string(e.Code)
	}
	return "posting: " + string(e.Code) + ": " + e.Msg
}

func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func fail(c Code, msg string) error { return &Error{Code: c, Msg: msg} }

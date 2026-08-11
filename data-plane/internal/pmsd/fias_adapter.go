package pmsd

import "fmt"

// Phase-3 pmsd is READ-ONLY. Outbound FIAS records are restricted to a hard allowlist of the verified
// read-only startup / subscription / heartbeat / resync records (Protel-FIAS spike §9/Gate-3A). Any
// financial Posting record (PS) or anything outside the allowlist is rejected at construction time.
//
// This allowlist is the single chokepoint every outbound frame must pass; TestOutboundAllowlist_RejectsPS
// and the dependency-graph check assert that no PS record can be built or transmitted by pmsd, and that
// pmsd allocates no P#, performs no financial retry, and has no Posting/Settlement/payment path.
var allowedOutboundRecords = map[string]struct{}{
	"LS": {}, // link start
	"LD": {}, // link description
	"LR": {}, // link record subscription (GI/GC/GO)
	"LA": {}, // link alive (heartbeat ack)
	"LE": {}, // link end
	"DR": {}, // database resync request (read-only)
}

// forbiddenOutboundRecords are never constructible by pmsd (financial / posting). Listed explicitly so a
// regression that reintroduces one is caught by the guard + tests rather than by review.
var forbiddenOutboundRecords = map[string]struct{}{
	"PS": {}, // financial Posting (Phase 4 only)
	"PA": {}, // posting answer (financial)
}

// ErrOutboundNotAllowed is returned for any record type not on the read-only allowlist.
type ErrOutboundNotAllowed struct{ Record string }

func (e ErrOutboundNotAllowed) Error() string {
	return fmt.Sprintf("pmsd: outbound record %q is not permitted (read-only connector; no financial posting)", e.Record)
}

// CheckOutbound validates a 2-letter FIAS record code against the read-only allowlist. It is the ONLY
// sanctioned way for the pmsd adapter to emit a frame; a forbidden/unknown record is rejected.
func CheckOutbound(record string) error {
	code := record
	if len(record) >= 2 {
		code = record[:2]
	}
	if _, bad := forbiddenOutboundRecords[code]; bad {
		return ErrOutboundNotAllowed{Record: code}
	}
	if _, ok := allowedOutboundRecords[code]; !ok {
		return ErrOutboundNotAllowed{Record: code}
	}
	return nil
}

// IsFinancialRecord reports whether a record code is a financial/posting type that Phase 3 must never emit.
func IsFinancialRecord(code string) bool {
	if len(code) >= 2 {
		code = code[:2]
	}
	_, bad := forbiddenOutboundRecords[code]
	return bad
}

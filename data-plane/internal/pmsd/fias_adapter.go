package pmsd

import "fmt"

// pmsd's FIAS adapter is read-only: this allowlist is the single chokepoint every outbound frame passes, and a
// financial Posting record (PS) or anything else outside it is rejected.
var allowedOutboundRecords = map[string]struct{}{
	"LS": {}, // link start
	"LD": {}, // link description
	"LR": {}, // link record subscription (GI/GC/GO)
	"LA": {}, // link alive (heartbeat ack)
	"LE": {}, // link end
	"DR": {}, // database resync request (read-only)
}

// forbiddenOutboundRecords are never constructible by pmsd; listed so a regression is caught by the guard.
var forbiddenOutboundRecords = map[string]struct{}{
	"PS": {}, // financial Posting
	"PA": {}, // posting answer (financial)
}

type ErrOutboundNotAllowed struct{ Record string }

func (e ErrOutboundNotAllowed) Error() string {
	return fmt.Sprintf("pmsd: outbound record %q is not permitted (read-only connector; no financial posting)", e.Record)
}

// CheckOutbound is the only sanctioned way for the adapter to emit a frame; forbidden and unknown records are rejected.
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

func IsFinancialRecord(code string) bool {
	if len(code) >= 2 {
		code = code[:2]
	}
	_, bad := forbiddenOutboundRecords[code]
	return bad
}

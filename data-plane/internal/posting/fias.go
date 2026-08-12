package posting

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// FIAS financial records, built ONLY from the accepted Phase-0 / Gate-3A contract (§9, §9a). Nothing here
// is inferred from observing a PMS: the field order, the fixed values and the AS catalog are the ones the
// contract records, and anything outside them is refused rather than guessed.
//
// PS field order is fixed and verified:   RN, G#, TA, PT, SO, CT, P#, WS
//
//	PT = D            (debit — the only posting type this system is authorized to send)
//	SO = WIFI         (source)
//	WS = STAYCONNECT  (workstation)
//	TA                integer MINOR UNITS. No decimal point, no thousands separator.
//	CT                the property-configured posting code, bounded and wire-safe.
//	P#                the protocol-attempt reference. NOT business idempotency.
//
// NO CURRENCY CODE IS TRANSMITTED. FIAS carries no currency field, which is exactly why the currency has to
// be pinned, compared and proven equal in the database before a byte is built: once the amount is on the
// wire there is nothing left to say which currency it was.
const (
	RecordPS = "PS"
	RecordPA = "PA"

	fixedPostingType = "D"
	fixedSource      = "WIFI"
	fixedWorkstation = "STAYCONNECT"

	// Contract section 9a: CT is bounded at 20 characters on this wire. 0011 used 32, which is not a
	// bound the contract grants -- a PMS that truncated silently would post against a code nobody chose.
	maxCTLen = 20

	// Contract section 9a fixes TA at integer minor units with EXPONENT 2 for this posting path, and the
	// Gate-3A live evidence is a USD 1.00 debit at exponent 2. The interface currency MODEL still allows
	// 0..4 because real ISO-4217 currencies do; this is the bound on what may be TRANSMITTED here, and
	// widening it would mean inventing protocol behaviour the evidence does not support.
	FIASCurrencyExponent = 2
)

// PSRequest is the fully-resolved input to a PS record. Every field is already pinned and verified; this
// type performs no lookups and reaches no database.
type PSRequest struct {
	RN          string
	GNumber     string
	AmountMinor int64
	PostingCode string // becomes CT
	PNumber     int64
}

// BuildPS renders the PS record body (without STX/ETX framing, which the transport applies).
//
// It validates every field again, even though the gate and the database already did. That is not
// redundancy for its own sake: this is the last place before the bytes exist, and a record that cannot be
// built is a charge that cannot be sent — which is the correct outcome for a field that would have been
// silently mangled.
func BuildPS(r PSRequest) (string, error) {
	if blank(r.RN) || wireUnsafe(r.RN) || len(r.RN) > maxWireField {
		return "", fail(ErrWireFieldInvalid, "RN")
	}
	if blank(r.GNumber) || wireUnsafe(r.GNumber) || len(r.GNumber) > maxWireField {
		return "", fail(ErrWireFieldInvalid, "G#")
	}
	if r.AmountMinor <= 0 {
		return "", fail(ErrAmountInvalid, "TA must be a positive integer in minor units")
	}
	ct := strings.TrimSpace(r.PostingCode)
	if ct == "" || wireUnsafe(ct) || len(ct) > maxCTLen {
		return "", fail(ErrWireFieldInvalid, "CT")
	}
	if r.PNumber <= 0 {
		return "", fail(ErrWireFieldInvalid, "P#")
	}
	var b strings.Builder
	b.WriteString(RecordPS)
	b.WriteString("|RN")
	b.WriteString(r.RN)
	b.WriteString("|G#")
	b.WriteString(r.GNumber)
	b.WriteString("|TA")
	b.WriteString(strconv.FormatInt(r.AmountMinor, 10))
	b.WriteString("|PT")
	b.WriteString(fixedPostingType)
	b.WriteString("|SO")
	b.WriteString(fixedSource)
	b.WriteString("|CT")
	b.WriteString(ct)
	b.WriteString("|P#")
	b.WriteString(strconv.FormatInt(r.PNumber, 10))
	b.WriteString("|WS")
	b.WriteString(fixedWorkstation)
	b.WriteString("|")
	return b.String(), nil
}

// asCatalog is the ONLY set of PA answer statuses this system accepts. An answer outside it is not
// interpreted charitably and is not treated as a failure either — it is ambiguous, and ambiguous is the one
// thing a financial correlator must never resolve on its own.
var asCatalog = map[string]struct{}{
	"OK": {}, "NG": {}, "NA": {}, "NP": {}, "NR": {}, "RY": {}, "UR": {},
}

// PA is a parsed posting answer.
type PA struct {
	PNumber int64
	AS      string
	Raw     string
}

// Posted reports whether this answer means the PMS accepted the charge. Only OK does. Everything else is
// either a refusal or a retry hint, and none of them is "probably fine".
func (a PA) Posted() bool { return a.AS == "OK" }

// ParsePA parses a PA record body.
//
// Correlation is by PMS Interface + P#, and by nothing else. RN is deliberately not read here at all: a
// room number identifies a room, not a posting, and two guests in the same room would correlate to each
// other's money. If P# is absent, unparseable, duplicated with different values, or the AS is outside the
// catalog, the answer is REFUSED rather than half-understood.
func ParsePA(body string) (PA, error) {
	if len(body) < 2 || body[:2] != RecordPA {
		return PA{}, fail(ErrPACorrelation, "not a PA record")
	}
	var (
		pnRaw, as string
		seenPN    bool
		seenAS    bool
	)
	for _, tok := range strings.Split(body[2:], "|") {
		if len(tok) < 2 {
			continue
		}
		id, val := tok[:2], tok[2:]
		switch id {
		case "P#":
			if seenPN && val != pnRaw {
				return PA{}, fail(ErrPAAmbiguous, "PA carries two different P# values")
			}
			pnRaw, seenPN = val, true
		case "AS":
			if seenAS && val != as {
				return PA{}, fail(ErrPAAmbiguous, "PA carries two different AS values")
			}
			as, seenAS = val, true
		}
	}
	if !seenPN || pnRaw == "" {
		return PA{}, fail(ErrPACorrelation, "PA carries no P#")
	}
	pn, err := strconv.ParseInt(pnRaw, 10, 64)
	if err != nil || pn <= 0 {
		return PA{}, fail(ErrPACorrelation, "PA P# is not a positive integer")
	}
	if !seenAS {
		return PA{}, fail(ErrPAStatusUnknown, "PA carries no AS")
	}
	if _, ok := asCatalog[as]; !ok {
		return PA{}, fail(ErrPAStatusUnknown, fmt.Sprintf("AS %q is not in the approved catalog", as))
	}
	return PA{PNumber: pn, AS: as, Raw: body}, nil
}

// FormatCT is unused on the wire path but documents the bound for callers assembling posting codes.
func FormatCT(code string) string {
	code = strings.TrimSpace(code)
	if len(code) > maxCTLen {
		return code[:maxCTLen]
	}
	return code
}

// AnswerDeadline is the bound after which a transmitted PS with no matched PA is UNKNOWN. It is a BOUND,
// not a retry timer: when it expires the attempt becomes UNKNOWN and NOTHING is resent.
const AnswerDeadline = 30 * time.Second

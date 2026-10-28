package posting

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// PS field order is fixed: RN, G#, TA, PT, SO, CT, P#, WS. PT=D (debit is the only type sent), SO=WIFI,
// WS=STAYCONNECT, TA is integer minor units, CT the bounded posting code, P# the attempt reference (not
// idempotency). FIAS carries no currency, so currency must be pinned and proven equal in the database before
// a byte is built.
const (
	RecordPS = "PS"
	RecordPA = "PA"

	fixedPostingType = "D"
	fixedSource      = "WIFI"
	fixedWorkstation = "STAYCONNECT"

	// CT is bounded at 20 characters on this wire; a PMS that truncated silently would post against a code nobody chose.
	maxCTLen = 20

	// Only exponent 2 has been observed on this wire. The currency model allows 0..4; widening what is
	// transmitted would invent protocol behaviour.
	FIASCurrencyExponent = 2
)

type PSRequest struct {
	RN          string
	GNumber     string
	AmountMinor int64
	PostingCode string // becomes CT
	PNumber     int64
}

// BuildPS renders the PS body without framing. It revalidates every field because it is the last place before
// the bytes exist; an unbuildable record is a charge that cannot be sent.
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

// asCatalog is the only accepted set of PA statuses; an answer outside it is ambiguous, never resolved as success or failure.
var asCatalog = map[string]struct{}{
	"OK": {}, "NG": {}, "NA": {}, "NP": {}, "NR": {}, "RY": {}, "UR": {},
}

type PA struct {
	PNumber int64
	AS      string
	Raw     string
}

// Posted is true only for OK.
func (a PA) Posted() bool { return a.AS == "OK" }

// ParsePA correlates by interface and P# only; RN is deliberately ignored since two guests in one room would
// share money. A missing, unparseable or conflicting P#, or an AS outside the catalog, is refused.
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

// AnswerDeadline is the bound after which a transmitted PS with no matched PA is UNKNOWN; it is not a retry timer.
const AnswerDeadline = 30 * time.Second

// settleTimeout bounds recording an attempt's outcome, which runs detached from the worker's cancellation.
const settleTimeout = 10 * time.Second

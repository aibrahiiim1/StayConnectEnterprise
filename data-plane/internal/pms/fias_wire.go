package pms

import (
	"bufio"
	"io"
)

// Exported FIAS wire primitives so the Phase-3 read-only connector runtime (internal/pmsd) can REUSE the
// accepted framing/parser rather than build a second stack. These are thin wrappers over the existing
// unexported helpers; behavior (STX/ETX framing, pipe-separated 2-char field ids) is unchanged.

// STX / ETX are the FIAS record frame delimiters.
const (
	STX = stx
	ETX = etx
)

// WriteFramedRecord writes one STX<body>ETX frame to w. This is the low-level frame writer; the pmsd
// adapter routes every outbound frame through its own allowlist chokepoint before calling this.
func WriteFramedRecord(w io.Writer, body string) error {
	buf := make([]byte, 0, len(body)+2)
	buf = append(buf, STX)
	buf = append(buf, body...)
	buf = append(buf, ETX)
	_, err := w.Write(buf)
	return err
}

// ReadFramedRecord reads one STX..ETX-bracketed record body (ETX stripped) from br.
func ReadFramedRecord(br *bufio.Reader) (string, error) { return readFramedRecord(br) }

// NOTE: pmsd Event ingestion no longer uses a permissive field parser. The connector parses GI/GC/GO under a
// STRICT grammar (internal/pmsd/strict_parse.go) that rejects malformed/empty/duplicate-ambiguous tokens
// instead of silently skipping them. The pms-internal parseFields (used by the legacy connectors in this
// package) is unchanged.

// RecordID returns the leading 2-char record id of a record body (e.g. "GI"), or "" if too short.
func RecordID(body string) string {
	if len(body) < 2 {
		return ""
	}
	return body[:2]
}

// Verified read-only record builders (reused shapes from the accepted ProtelFIAS handshake).

// BuildLS is the link-start record.
func BuildLS(dateYYMMDD, timeHHMMSS string) string {
	return "LS|DA" + dateYYMMDD + "|TI" + timeHHMMSS + "|"
}

// BuildLD is the link-description record.
func BuildLD(dateYYMMDD, timeHHMMSS, ifcName, version string) string {
	return "LD|DA" + dateYYMMDD + "|TI" + timeHHMMSS + "|" + ifcName + "|V#" + version + "|RT4|"
}

// The interface identity this connector declares in LD. IF (Interface Family) is the field FIAS says the PMS
// uses "to determine the screen display for the requested interface type and to activate/deactivate certain
// functionalities" (FIAS 2.20, IF - Interface Types). IFPB / V#1.13 is the handshake verified against this
// property's Protel on 150.0.0.18:5003 for the read-only feed AND the live posting (Phase-0 spike, Gate 1B and
// Gate 3A; the ProtelFIAS connector's defaults).
//
// THE pmsd REWRITE HAD DROPPED IT. It sent the bare token "pmsd" where the IF field belongs -- not a FIAS field
// at all -- and V#1, so the link declared no interface family. VerifiedLD restores the verified record exactly.
const (
	VerifiedInterfaceID = "IFPB"
	VerifiedIFCVersion  = "1.13"
)

// VerifiedLD is the link-description record of the verified handshake.
func VerifiedLD(dateYYMMDD, timeHHMMSS string) string {
	return BuildLD(dateYYMMDD, timeHHMMSS, VerifiedInterfaceID, VerifiedIFCVersion)
}

// BuildLRs are the read-only record subscriptions (GI/GC/GO).
func BuildLRs() []string {
	return []string{
		// A0 and A1 are FIAS user-definable fields (FIAS 2.20: "requires special configuration in PMS"). The
		// legacy StayConnect+ Protel client requested exactly these on GI and GC and read A0 as the travel agent
		// and A1 as VIP (stayconnectplus libs/fias/protel_client.py FL_GI/FL_GC, libs/pms_protel.py). They are
		// guest attributes, never identity. Field order in LR is not significant (FIAS 2.20, Link Records).
		"LR|RIGI|FLRNG#GNGFGAGDA0A1|",
		"LR|RIGC|FLRNG#GNGFGAGDA0A1|",
		"LR|RIGO|FLRNG#|",
	}
}

// BuildLA is the link-alive acknowledgement.
func BuildLA() string { return "LA|" }

// BuildDR is the read-only database-resync request.
func BuildDR() string { return "DR|" }

// BuildLE is the link-end record (controlled shutdown).
func BuildLE() string { return "LE|" }

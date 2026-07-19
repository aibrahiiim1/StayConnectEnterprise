package pms

import (
	"bufio"
	"io"
	"strings"
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

// ParseFields walks a "|FFvalue|FFvalue|" record tail into a map of 2-char field id → value.
func ParseFields(tail string) map[string]string { return parseFields(tail) }

// ParseFieldPairs walks a record body into an ORDERED list of (2-char code, value) pairs, preserving
// DUPLICATE occurrences and present-but-empty values ("GN" → {"GN",""}). An empty segment (from "||") is
// skipped (an absent field emits no pair). Unlike ParseFields (a map), this keeps the complete record
// content in order so a fingerprint over it can distinguish reordered/duplicate fields. Reuses the same
// pipe-splitting + 2-char-code convention as the accepted parser.
func ParseFieldPairs(body string) [][2]string {
	var out [][2]string
	for _, seg := range strings.Split(body, "|") {
		if len(seg) < 2 {
			continue
		}
		out = append(out, [2]string{seg[:2], seg[2:]})
	}
	return out
}

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

// BuildLRs are the read-only record subscriptions (GI/GC/GO).
func BuildLRs() []string {
	return []string{
		"LR|RIGI|FLRNG#GNGFGAGD|",
		"LR|RIGC|FLRNG#GNGFGAGD|",
		"LR|RIGO|FLRNG#|",
	}
}

// BuildLA is the link-alive acknowledgement.
func BuildLA() string { return "LA|" }

// BuildDR is the read-only database-resync request.
func BuildDR() string { return "DR|" }

// BuildLE is the link-end record (controlled shutdown).
func BuildLE() string { return "LE|" }

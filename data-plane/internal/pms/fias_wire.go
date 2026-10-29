package pms

import (
	"bufio"
	"io"
)

// Exported FIAS wire primitives so internal/pmsd reuses the accepted framing and parser instead of a second stack.

const (
	STX = stx
	ETX = etx
)

// The pmsd adapter routes every outbound frame through its own allowlist before calling this.
func WriteFramedRecord(w io.Writer, body string) error {
	buf := make([]byte, 0, len(body)+2)
	buf = append(buf, STX)
	buf = append(buf, body...)
	buf = append(buf, ETX)
	_, err := w.Write(buf)
	return err
}

func ReadFramedRecord(br *bufio.Reader) (string, error) { return readFramedRecord(br) }

// pmsd parses GI/GC/GO under a STRICT grammar (internal/pmsd/strict_parse.go) that rejects malformed, empty and
// ambiguous tokens.

func RecordID(body string) string {
	if len(body) < 2 {
		return ""
	}
	return body[:2]
}

func BuildLS(dateYYMMDD, timeHHMMSS string) string {
	return "LS|DA" + dateYYMMDD + "|TI" + timeHHMMSS + "|"
}

func BuildLD(dateYYMMDD, timeHHMMSS, ifcName, version string) string {
	return "LD|DA" + dateYYMMDD + "|TI" + timeHHMMSS + "|" + ifcName + "|V#" + version + "|RT4|"
}

// IFPB / V#1.13 is the handshake verified against Protel (read-only feed and posting); IF tells the PMS which interface
// family to activate. A bare "pmsd" in IF is not a FIAS field and declares no family.
const (
	VerifiedInterfaceID = "IFPB"
	VerifiedIFCVersion  = "1.13"
)

func VerifiedLD(dateYYMMDD, timeHHMMSS string) string {
	return BuildLD(dateYYMMDD, timeHHMMSS, VerifiedInterfaceID, VerifiedIFCVersion)
}

func BuildLRs() []string {
	return []string{
		// A0 and A1 are user-definable FIAS fields the legacy Protel client requested on GI/GC (A0 travel agent, A1
		// VIP); they are guest attributes, never identity.
		"LR|RIGI|FLRNG#GNGFGAGDA0A1|",
		"LR|RIGC|FLRNG#GNGFGAGDA0A1|",
		"LR|RIGO|FLRNG#|",
	}
}

func BuildLA() string { return "LA|" }

func BuildDR() string { return "DR|" }

func BuildLE() string { return "LE|" }

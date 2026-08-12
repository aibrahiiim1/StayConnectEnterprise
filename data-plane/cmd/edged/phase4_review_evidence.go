package main

// The Manual Review EVIDENCE contract.
//
// The first version of this endpoint accepted any non-empty JSON object and wrote it verbatim into the
// immutable financial review ledger. That satisfied §15's "evidence is mandatory" and violated §11's
// "no PII, card data, credentials or raw PMS secrets in logs/metrics/audit" — because an append-only
// ledger is the worst possible place to discover a secret later. You cannot redact an immutable row.
//
// So evidence is now a CLOSED, BOUNDED, STRUCTURED shape rather than a blob. An operator records where
// they looked, what reference identifies it, and a short note. That is what the external-folio
// reconciliation workflow actually needs, and it is not a place a credential can hide.
//
// The rejection rules below are deliberately conservative. A refusal costs an operator a re-typed note;
// a false accept costs a permanent secret in a financial audit trail.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// evidenceSourceTypes is the closed set of places a financial reviewer can have looked. "OTHER_DOCUMENTED"
// exists so an unanticipated but legitimate source is recordable without opening the shape back up — it
// still carries only a bounded reference and note.
var evidenceSourceTypes = map[string]string{
	"PMS_FOLIO_INSPECTION":      "the operator opened the guest folio in the PMS and looked",
	"PMS_REPORT":                "a PMS report or export was consulted",
	"FRONT_OFFICE_CONFIRMATION": "front office confirmed the folio state directly",
	"NIGHT_AUDIT":               "the night-audit run was consulted",
	"RECONCILIATION_EXPORT":     "a reconciliation export was compared",
	"PROVIDER_DASHBOARD":        "the payment provider's own dashboard was consulted",
	"OTHER_DOCUMENTED":          "another documented source, described in the note",
}

const (
	maxEvidenceReference = 120
	maxEvidenceNote      = 500
)

// reviewEvidenceInput is exactly what a reviewer may record. There is no free-form JSON member, so there
// is nowhere for a raw provider payload or a PMS frame to be attached.
type reviewEvidenceInput struct {
	SourceType string `json:"source_type"`
	// Reference identifies the external artefact: a folio number, a report id, a reconciliation batch, a
	// provider reference. It is an IDENTIFIER, not a payload.
	Reference string `json:"reference"`
	// Note is a short sanitized human sentence. It is bounded because an unbounded note is a blob.
	Note string `json:"note"`
	// VerifiedAt is when the operator actually looked, which is not the same as when they clicked.
	VerifiedAt string `json:"verified_at"`
}

// secretShaped matches the patterns that must never reach an append-only financial ledger. These are
// deliberately broad: the cost of a false positive is a re-typed note.
var secretShaped = []struct {
	name string
	re   *regexp.Regexp
}{
	{"a credential keyword", regexp.MustCompile(`(?i)\b(pass(word|phrase)?|pwd|secret|api[-_ ]?key|apikey|token|bearer|authorization|credential|private[-_ ]?key)\b`)},
	{"a key or token literal", regexp.MustCompile(`(?i)(-----BEGIN|eyJ[A-Za-z0-9_-]{10,}|sk_live_|pk_live_|whsec_|xox[baprs]-)`)},
	{"something shaped like a card number", regexp.MustCompile(`\b(?:\d[ -]?){13,19}\b`)},
	{"a card verification value", regexp.MustCompile(`(?i)\b(cvv|cvc|cvv2|card[-_ ]?number|pan)\b`)},
	{"a raw FIAS frame", regexp.MustCompile(`(?i)(^|[|\x02\x03])(PS|PA|GI|GC|GO|LS|LD|LR)\|`)},
	{"a raw JSON or XML payload", regexp.MustCompile(`(^\s*[{\[])|(<\?xml)|(</[A-Za-z])`)},
}

// evidenceError is a validation refusal an operator can act on.
type evidenceError struct{ msg string }

func (e evidenceError) Error() string { return e.msg }

// validateEvidence checks the structured evidence and returns the canonical JSON that will be persisted.
//
// required reports whether evidence is mandatory for this action (every terminal action; ESCALATE decides
// nothing and may be raised without it).
func validateEvidence(in reviewEvidenceInput, required bool) (string, error) {
	empty := strings.TrimSpace(in.SourceType) == "" && strings.TrimSpace(in.Reference) == "" &&
		strings.TrimSpace(in.Note) == "" && strings.TrimSpace(in.VerifiedAt) == ""
	if empty {
		if required {
			return "", evidenceError{"a terminal financial decision must record its evidence"}
		}
		return "{}", nil
	}

	src := strings.TrimSpace(in.SourceType)
	if _, ok := evidenceSourceTypes[src]; !ok {
		return "", evidenceError{"source_type must be one of: " + strings.Join(evidenceSourceTypeNames(), ", ")}
	}
	ref := strings.TrimSpace(in.Reference)
	note := strings.TrimSpace(in.Note)
	if ref == "" {
		return "", evidenceError{"reference is required: it identifies the external artefact that was checked"}
	}
	if len(ref) > maxEvidenceReference {
		return "", evidenceError{fmt.Sprintf("reference must be at most %d characters", maxEvidenceReference)}
	}
	if len(note) > maxEvidenceNote {
		return "", evidenceError{fmt.Sprintf("note must be at most %d characters", maxEvidenceNote)}
	}
	if src == "OTHER_DOCUMENTED" && note == "" {
		return "", evidenceError{"OTHER_DOCUMENTED requires a note describing the source"}
	}
	for label, v := range map[string]string{"reference": ref, "note": note} {
		if err := checkSafeText(label, v); err != nil {
			return "", err
		}
	}

	verified := strings.TrimSpace(in.VerifiedAt)
	if verified != "" {
		t, err := time.Parse(time.RFC3339, verified)
		if err != nil {
			return "", evidenceError{"verified_at must be an RFC3339 timestamp"}
		}
		if t.After(time.Now().Add(5 * time.Minute)) {
			return "", evidenceError{"verified_at cannot be in the future"}
		}
		verified = t.UTC().Format(time.RFC3339)
	}

	out := map[string]string{"source_type": src, "reference": ref}
	if note != "" {
		out["note"] = note
	}
	if verified != "" {
		out["verified_at"] = verified
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "", evidenceError{"evidence could not be encoded"}
	}
	return string(raw), nil
}

// checkSafeText refuses control characters, unbounded whitespace abuse and anything secret-shaped.
func checkSafeText(field, v string) error {
	for _, r := range v {
		if r == '\n' || r == '\r' || r == '\t' {
			return evidenceError{field + " must be a single line"}
		}
		if unicode.IsControl(r) {
			return evidenceError{field + " must not contain control characters"}
		}
	}
	for _, s := range secretShaped {
		if s.re.MatchString(v) {
			return evidenceError{field + " looks like it contains " + s.name +
				"; financial review evidence is an immutable audit record and must never carry secrets, " +
				"card data or raw payloads. Record a reference to the artefact instead."}
		}
	}
	return nil
}

func evidenceSourceTypeNames() []string {
	return []string{"PMS_FOLIO_INSPECTION", "PMS_REPORT", "FRONT_OFFICE_CONFIRMATION", "NIGHT_AUDIT",
		"RECONCILIATION_EXPORT", "PROVIDER_DASHBOARD", "OTHER_DOCUMENTED"}
}

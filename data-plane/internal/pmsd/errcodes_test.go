package pmsd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// canaries are values that MUST NEVER appear in a persisted/exported error code or structured log field.
var canaries = []string{
	"hunter2password", "Sm1thR00mKey", // credential / endpoint password
	"room-1408",                     // room number
	"John Q. Guest",                 // guest name
	"RES-88213371",                  // reservation number
	"FOLIO-55510",                   // folio number
	"pms.internal.example.com:5010", // endpoint host:port
}

func TestClassify_NeverLeaksRawText(t *testing.T) {
	for _, cn := range canaries {
		raw := fmt.Errorf("dial tcp %s failed for guest %s res %s", cn, cn, cn)
		code := Classify(raw)
		if !code.Valid() {
			t.Fatalf("Classify produced an out-of-vocabulary code for %q", cn)
		}
		if strings.Contains(code.String(), cn) {
			t.Errorf("code %q leaked canary %q", code, cn)
		}
		if code != CodeUnclassified {
			t.Errorf("unstructured raw error should classify as UNCLASSIFIED, got %q", code)
		}
	}
}

func TestTypedErr_HidesRawDetail(t *testing.T) {
	for _, cn := range canaries {
		e := coded(CodeDialFailed, fmt.Errorf("connect %s", cn))
		if strings.Contains(e.Error(), cn) {
			t.Errorf("typedErr.Error() leaked canary %q: %s", cn, e.Error())
		}
		if Classify(e) != CodeDialFailed {
			t.Errorf("Classify(coded) = %q, want DIAL_FAILED", Classify(e))
		}
		// Unwrap still exposes the raw error for LOCAL debugging only (never persisted).
		if !strings.Contains(errors.Unwrap(e).Error(), cn) {
			t.Errorf("Unwrap should retain raw detail for local debug")
		}
	}
}

func TestDebugRedact_ScrubsSecretsAndEndpoints(t *testing.T) {
	cases := []string{
		"password=hunter2password", "secret_token=abcdefghijABCDEFGHIJ0123456789",
		"pms.internal.example.com:5010", "192.168.10.20:5010",
	}
	for _, c := range cases {
		got := debugRedact(errors.New(c))
		if !strings.Contains(got, "[REDACTED]") {
			t.Errorf("debugRedact(%q) did not redact: %q", c, got)
		}
	}
}

func TestClassify_StructuralCodes(t *testing.T) {
	if Classify(context.Canceled) != CodeContextCanceled {
		t.Error("context.Canceled must map to CONTEXT_CANCELED")
	}
	if Classify(ErrStaleGeneration) != CodeRuntimeGenStale {
		t.Error("ErrStaleGeneration must map to RUNTIME_GENERATION_STALE")
	}
	if Classify(nil) != CodeNone {
		t.Error("nil must map to CodeNone")
	}
}

func TestAllCodes_InVocabulary(t *testing.T) {
	for c := range codeSet {
		if !c.Valid() {
			t.Errorf("code %q not valid per its own set", c)
		}
	}
	// an invented code is rejected
	if Code("SOMETHING_ELSE").Valid() {
		t.Error("out-of-set code reported valid")
	}
	if Code("SOMETHING_ELSE").String() != string(CodeUnclassified) {
		t.Error("out-of-set code must render as UNCLASSIFIED")
	}
}

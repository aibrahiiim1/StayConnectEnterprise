package pmsd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// canaries must never appear in a persisted error code or log line.
var canaries = []string{
	"hunter2password",               // secret / password
	"svcuser",                       // endpoint username
	"Sm1thR00mKey",                  // endpoint password
	"room-1408",                     // room number
	"John Q. Guest",                 // guest name
	"RES-88213371",                  // reservation number
	"FOLIO-55510",                   // folio number
	"\x02GI|GN12345|room-1408\x03",  // raw GI frame bytes
	"AAAABBBBCCCCDDDDciphertext==",  // ciphertext blob
	"nonce-9f8e7d6c",                // nonce
	"kms-key-7b3a1f",                // key ID
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
		if !strings.Contains(errors.Unwrap(e).Error(), cn) {
			t.Errorf("Unwrap should retain raw detail for local debug")
		}
	}
}

// Even with a canary smuggled into the identity, Stage or LogEvent, nothing sensitive reaches the log line;
// invalid inputs render as fixed placeholders.
func TestLogEvent_NoCanaryInCapturedOutput(t *testing.T) {
	for _, cn := range canaries {
		var buf bytes.Buffer
		log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		raw := fmt.Errorf("dial %q endpoint failed frame=%q", cn, cn)
		logEvent(log, LogEvent(cn), Classify(coded(CodeDialFailed, raw)),
			SafeFields{InterfaceID: NewUUIDValue(cn), Generation: 7, Stage: Stage(cn), Attempt: 2})
		out := buf.String()
		if strings.Contains(out, cn) {
			t.Errorf("logEvent leaked canary %q into output: %s", cn, out)
		}
		if !strings.Contains(out, "DIAL_FAILED") {
			t.Errorf("expected code DIAL_FAILED in log, got: %s", out)
		}
		if !strings.Contains(out, "INVALID_UUID") || !strings.Contains(out, "INVALID_STAGE") || !strings.Contains(out, "INVALID_LOG_EVENT") {
			t.Errorf("invalid fields must render as placeholders, got: %s", out)
		}
	}
}

func TestLogEvent_PanicValueNeverRendered(t *testing.T) {
	for _, cn := range canaries {
		var buf bytes.Buffer
		log := slog.New(slog.NewJSONHandler(&buf, nil))
		_ = recoverToLog(log, cn)
		if strings.Contains(buf.String(), cn) {
			t.Errorf("panic value canary %q leaked: %s", cn, buf.String())
		}
		if !strings.Contains(buf.String(), "WORKER_PANIC_RECOVERED") {
			t.Errorf("expected WORKER_PANIC_RECOVERED, got: %s", buf.String())
		}
	}
}

// recoverToLog models the supervisor's panic path: the recovered value is discarded.
func recoverToLog(log *slog.Logger, panicValue string) (rec any) {
	rec = panicValue // stand-in for recover()
	logEvent(log, EventWorkerPanicRecovered, CodePanicRecovered,
		SafeFields{InterfaceID: NewUUIDValue("aaaaaaaa-0000-4000-8000-000000000001"), Generation: 3, Stage: StageServe, Attempt: 1})
	return rec
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
	if Code("SOMETHING_ELSE").Valid() {
		t.Error("out-of-set code reported valid")
	}
	if Code("SOMETHING_ELSE").String() != string(CodeUnclassified) {
		t.Error("out-of-set code must render as UNCLASSIFIED")
	}
}

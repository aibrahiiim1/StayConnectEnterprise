package pmsd

// Progress lines must make a recovered feed audible, through the same closed vocabulary and bounded fields
// so nothing PMS-, guest- or secret-derived can ride on them.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// A reconnect is INFO, not ERROR, so severity-filtered journals still show the feed returning.
func TestProgress_TheSuccessPathIsAudibleAndIsNotAnError(t *testing.T) {
	for _, ev := range []LogEvent{EventWorkerConnected, EventWorkerResyncRequested, EventWorkerResyncPublished} {
		if !ev.Valid() {
			t.Fatalf("%s is not in the closed vocabulary, so it would render as INVALID_LOG_EVENT", ev)
		}
		var buf bytes.Buffer
		log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		logProgress(log, ev, SafeFields{
			InterfaceID: NewUUIDValue("ddff5d07-0000-4000-8000-000000000001"),
			Generation:  184, Stage: StageServe, Records: 1141})

		var line map[string]any
		if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
			t.Fatalf("%s: unparseable log line %q: %v", ev, buf.String(), err)
		}
		if line["level"] != "INFO" {
			t.Fatalf("%s logged at %v; a successful reconnect is not an error", ev, line["level"])
		}
		if line["msg"] != string(ev) {
			t.Fatalf("msg = %v, want %s", line["msg"], ev)
		}
		if line["code"] != string(CodeNone) {
			t.Fatalf("%s carried code %v; a success carries no error code", ev, line["code"])
		}
		if fmt.Sprint(line["generation"]) != "184" {
			t.Fatalf("%s lost the generation: %v", ev, line["generation"])
		}
	}
}

// The publication line carries how many records arrived, never which (guest data).
func TestProgress_PublicationReportsHowManyNeverWhich(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	logProgress(log, EventWorkerResyncPublished, SafeFields{
		InterfaceID: NewUUIDValue("ddff5d07-0000-4000-8000-000000000001"),
		Generation:  184, Stage: StageServe, Records: 590})
	out := buf.String()
	if !strings.Contains(out, `"records":590`) {
		t.Fatalf("the publication line does not say how many records arrived: %s", out)
	}
	var line map[string]any
	_ = json.Unmarshal(buf.Bytes(), &line)
	allowed := map[string]bool{"time": true, "level": true, "msg": true, "code": true,
		"interface": true, "generation": true, "stage": true, "records": true}
	for k := range line {
		if !allowed[k] {
			t.Fatalf("the progress line grew an unbounded field %q: %s", k, out)
		}
	}
}

// Every free-form input is poisoned with a canary; none may reach the output.
func TestProgress_NoCanaryInCapturedOutput(t *testing.T) {
	for _, cn := range canaries {
		var buf bytes.Buffer
		log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		logProgress(log, LogEvent(cn), SafeFields{
			InterfaceID: NewUUIDValue(cn), Generation: 7, Stage: Stage(cn), Records: 3})
		out := buf.String()
		if strings.Contains(out, cn) {
			t.Fatalf("logProgress leaked canary %q: %s", cn, out)
		}
		if !strings.Contains(out, "INVALID_UUID") || !strings.Contains(out, "INVALID_STAGE") ||
			!strings.Contains(out, "INVALID_LOG_EVENT") {
			t.Fatalf("invalid fields must render as placeholders: %s", out)
		}
	}
}

// A nil logger is a no-op: observability must never kill the daemon.
func TestProgress_NilLoggerIsSafe(t *testing.T) {
	logProgress(nil, EventWorkerConnected, SafeFields{})
}

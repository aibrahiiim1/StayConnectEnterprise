package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func useEtcDir(t *testing.T) string {
	t.Helper()
	old := etcStayconnectDir
	etcStayconnectDir = filepath.Join(t.TempDir(), "stayconnect")
	t.Cleanup(func() { etcStayconnectDir = old })
	return etcStayconnectDir
}

var sha64 = strings.Repeat("ab", 32)

func TestTheMarkerAdvancesIsWorldReadableAndNeverMovesBack(t *testing.T) {
	dir := useEtcDir(t)
	if g, err := readMarkerGeneration(); err != nil || g != 0 {
		t.Fatalf("no marker = %d, %v", g, err)
	}
	if err := writeMarker(1, sha64); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, financialMarker))
	if err != nil {
		t.Fatal(err)
	}
	// edged is not root and must be able to read it: a 0600 marker disarmed the detector once
	if fi.Mode().Perm()&0o044 != 0o044 {
		t.Errorf("marker mode %v is not world-readable", fi.Mode().Perm())
	}
	var m map[string]any
	b, _ := os.ReadFile(filepath.Join(dir, financialMarker))
	if err := json.Unmarshal(b, &m); err != nil || m["restore_generation"].(float64) != 1 || m["manifest_sha256"] != sha64 {
		t.Fatalf("marker content = %s (%v)", b, err)
	}
	if err := writeMarker(1, sha64); err == nil {
		t.Error("the marker was written at the generation it already had")
	}
	if err := writeMarker(0, sha64); err == nil {
		t.Error("the marker moved backwards")
	}
	if err := writeMarker(2, "not-a-digest"); err == nil {
		t.Error("the marker accepted something that is not a digest")
	}
	if err := writeMarker(2, sha64); err != nil {
		t.Fatal(err)
	}
	if g, _ := readMarkerGeneration(); g != 2 {
		t.Fatalf("generation = %d", g)
	}
	_ = os.WriteFile(filepath.Join(dir, financialMarker), []byte("{not json"), 0o644)
	if _, err := readMarkerGeneration(); err == nil {
		t.Error("a corrupt marker was read as generation 0")
	}
}

func TestMarkerAdvanceCommand(t *testing.T) {
	useEtcDir(t)
	if rc := markerAdvanceMain([]string{"--generation", "1", "--manifest-sha", sha64}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if rc := markerAdvanceMain([]string{"--generation", "1", "--manifest-sha", sha64}); rc == 0 {
		t.Fatal("a repeated advance succeeded")
	}
}

func TestTheQuiesceIsProvenNotAssumed(t *testing.T) {
	oldStop, oldState := stopService, serviceState
	defer func() { stopService, serviceState = oldStop, oldState }()
	states := map[string]string{}
	stopService = func(_ context.Context, svc string) error {
		if states[svc] == "stop-fails" {
			return errors.New("refused")
		}
		return nil
	}
	serviceState = func(_ context.Context, svc string) string {
		if s, ok := states[svc]; ok && s != "stop-fails" {
			return s
		}
		return "inactive"
	}
	if err := proveQuiesced(context.Background()); err != nil {
		t.Fatalf("all stopped: %v", err)
	}
	states["stayconnect-pmsd"] = "active" // stop returned success but the service is still running
	if err := proveQuiesced(context.Background()); err == nil || !strings.Contains(err.Error(), "stayconnect-pmsd (active)") {
		t.Fatalf("a running financial writer was not caught: %v", err)
	}
	states["stayconnect-pmsd"] = "activating"
	if err := proveQuiesced(context.Background()); err == nil {
		t.Fatal("a service that is starting counted as stopped")
	}
	states["stayconnect-pmsd"] = "stop-fails"
	if err := proveQuiesced(context.Background()); err == nil || !strings.Contains(err.Error(), "stop failed") {
		t.Fatalf("a failed stop was not reported: %v", err)
	}
	states["stayconnect-pmsd"] = "failed"
	if err := proveQuiesced(context.Background()); err != nil {
		t.Fatalf("a failed (not running) service blocked the restore: %v", err)
	}
}

func TestEveryFinancialWriterIsInTheQuiesce(t *testing.T) {
	for _, svc := range []string{"stayconnect-edged", "stayconnect-pmsd", "stayconnect-acctd", "stayconnect-portald"} {
		found := false
		for _, d := range dependentServices {
			if d == svc {
				found = true
			}
		}
		if !found {
			t.Errorf("%s can write financial state and is not stopped before a restore", svc)
		}
	}
}

func TestRecordingARestoreRefusesBadIdentities(t *testing.T) {
	good := "36f3ba78-f8d8-41b6-aa2e-50d797e2d9c8"
	for _, c := range [][3]string{{"", good, sha64}, {good, "x", sha64}, {good, good, "short"}, {good + "'; DROP", good, sha64}} {
		if err := recordSupportedRestore(context.Background(), "db", c[0], c[1], 3, c[2], time.Time{}, "by"); err == nil {
			t.Errorf("accepted %v", c)
		}
	}
}

// The ORDER is the guarantee: services proven stopped, then the marker advanced, both before the rename that makes the restored
// database live; the restore is recorded only after the integrity check, and a failure there rolls the swap back.
func TestTheFinancialStepsAreInTheirRequiredOrder(t *testing.T) {
	b, err := os.ReadFile("restore.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	idx := func(sub string) int {
		i := strings.Index(src, sub)
		if i < 0 {
			t.Fatalf("restore.go does not contain %q", sub)
		}
		return i
	}
	quiesce := idx("proveQuiesced(ctx)")
	marker := idx("advanceMarker(ctx, nextGen,")
	swap := idx(`"ALTER DATABASE " + staging + " RENAME TO " + pgDatabase`)
	integrity := idx("s.restoreIntegrity(ctx)")
	hyper := idx("assertHypertables(ctx, pgDatabase)")
	record := idx("recordSupportedRestore(ctx,")
	if !(quiesce < marker && marker < swap && swap < integrity && integrity < hyper && hyper < record) {
		t.Fatalf("order wrong: quiesce %d marker %d swap %d integrity %d hypertables %d record %d", quiesce, marker, swap, integrity, hyper, record)
	}
	if !strings.Contains(src, `rollback("financial_recovery"`) || !strings.Contains(src, `rollback("hypertables"`) {
		t.Error("a failure to record the restore or to register the hypertables does not roll the swap back")
	}
	if !strings.Contains(mustRead(t, "backups.go"), "financialMarker") {
		t.Error("the configuration archive does not name the marker it must leave out")
	}
}

func TestTheDatabaseGrantIsCarriedAcrossTheSwap(t *testing.T) {
	if !strings.Contains(privilegeCaptureSQL, "ON DATABASE") || !strings.Contains(privilegeCaptureSQL, "aclexplode(d.datacl)") {
		t.Fatal("the privilege capture does not include database-level grants, so GRANT CREATE ON DATABASE is lost in a restore")
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

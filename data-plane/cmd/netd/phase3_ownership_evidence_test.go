package main

// THE FAILURE THIS FILE REPRODUCES ACTUALLY HAPPENED.
//
// PRE-LIVE appliance, 2026-08-31. /var/lib/kea/kea-leases4.csv.pid was zero bytes. memfile refuses to open its
// lease database while it cannot read a PID from that file, so kea-dhcp4 came up with NO LEASE MANAGER - and
// then ran for three days answering status-get normally, holding its control socket open, and refusing every
// single lease command with:
//
//     { "result": 1, "text": "no current lease manager is available" }
//
// It had issued no lease for two days. The health surface asked only whether Kea answered status-get, so it
// reported healthy the whole time. Address ownership - the rule that stops the appliance authorizing an
// address after the device holding it has gone - depends entirely on those leases, and it was silently
// deciding UNKNOWN for every session on the box.
//
// These tests hold the three conditions apart, because they need three different operator responses:
// the lease manager being gone, a query failing for any other reason, and the on-disk state that causes it.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// failingLeases is an evidence source that only ever fails, which is the whole of what these cases need.
type failingLeases struct{ err error }

func (f failingLeases) Leases() ([]KeaLease, error) { return nil, f.err }

func timeNowPlusMinute() time.Time { return time.Now().Add(time.Minute) }

type staticFiles struct {
	path string
	err  error
}

func (s staticFiles) LeaseFile() (string, error) { return s.path, s.err }

// THE .25 CONDITION, in Kea's own words.
func TestEvidence_LeaseManagerUnavailableIsItsOwnFault(t *testing.T) {
	src := failingLeases{err: errors.New("lease4-get-all: no current lease manager is available")}

	h := probeEvidence(context.Background(), src, nil)

	if h.Available {
		t.Fatal("evidence was reported available while every lease command was refused")
	}
	if h.Fault != evidenceLeaseManagerUnavailable {
		t.Fatalf("fault = %q, want %q", h.Fault, evidenceLeaseManagerUnavailable)
	}
	if h.Detail == "" {
		t.Fatal("the fault carries no detail, so an operator learns nothing from it")
	}
}

// Any other query failure is a DIFFERENT fault. A socket that has gone away is an ordinary outage; a server
// running without a lease manager is a silent one, and telling an operator they are the same thing sends them
// looking in the wrong place.
func TestEvidence_AnyOtherQueryFailureIsNotMistakenForIt(t *testing.T) {
	h := probeEvidence(context.Background(),
		failingLeases{err: errors.New("dial unix /run/kea/kea4-ctrl-socket: connect: no such file or directory")}, nil)

	if h.Available || h.Fault != evidenceQueryFailed {
		t.Fatalf("fault = %q available=%v, want %q", h.Fault, h.Available, evidenceQueryFailed)
	}
}

// THE ARTIFACT. When the query fails AND the memfile PID companion is empty, the on-disk cause outranks the
// symptom: that file is what an operator has to look at, and naming it turns three days of diagnosis into one
// line of output.
func TestEvidence_TheEmptyPIDFileIsNamedAsTheCause(t *testing.T) {
	dir := t.TempDir()
	leaseFile := filepath.Join(dir, "kea-leases4.csv")
	if err := os.WriteFile(leaseFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(leaseFile+".pid", nil, 0o600); err != nil { // zero bytes, exactly as found
		t.Fatal(err)
	}

	h := probeEvidence(context.Background(),
		failingLeases{err: errors.New("lease4-get-all: no current lease manager is available")},
		staticFiles{path: leaseFile})

	if h.Available {
		t.Fatal("evidence reported available")
	}
	if h.Fault != evidenceMemfileUnusable {
		t.Fatalf("fault = %q, want %q — the cause outranks the symptom", h.Fault, evidenceMemfileUnusable)
	}
	if !contains(h.Detail, leaseFile+".pid") || !contains(h.Detail, "EMPTY") {
		t.Fatalf("the detail does not name the artifact: %q", h.Detail)
	}
	// NOTHING WAS REPAIRED. A health check that quietly deletes the evidence hides a recurring fault and
	// destroys what the next operator needs.
	if _, err := os.Stat(leaseFile + ".pid"); err != nil {
		t.Fatalf("the probe removed the PID file: %v", err)
	}
	if _, err := os.Stat(leaseFile); err != nil {
		t.Fatalf("the probe touched the lease file: %v", err)
	}
}

// An ABSENT PID file is the normal, healthy case - nothing is holding the lease file - and must not be
// reported as a fault of its own.
func TestEvidence_AnAbsentPIDFileIsNotAFault(t *testing.T) {
	dir := t.TempDir()
	leaseFile := filepath.Join(dir, "kea-leases4.csv")
	if err := os.WriteFile(leaseFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := probeEvidence(context.Background(), fakeLeases{rows: []KeaLease{lease("10.0.0.5", "aa:bb:cc:dd:ee:ff")}},
		staticFiles{path: leaseFile})
	if !h.Available || h.Fault != evidenceOK {
		t.Fatalf("a healthy Kea was reported as %q: %s", h.Fault, h.Detail)
	}
	if h.Leases != 1 {
		t.Fatalf("leases = %d, want 1 — health must report what it actually read", h.Leases)
	}
}

// A netd with no lease source at all is a wiring fault, not a DHCP fault, and is named as such.
func TestEvidence_NoSourceIsReportedAsAConfigurationFault(t *testing.T) {
	h := probeEvidence(context.Background(), nil, nil)
	if h.Available || h.Fault != evidenceNotConfigured {
		t.Fatalf("fault = %q, want %q", h.Fault, evidenceNotConfigured)
	}
}

// THE PLANE MUST NOT REPORT ITSELF HEALTHY WHILE ITS OWNERSHIP AUTHORITY IS DOWN.
//
// This is the whole point. The applier goes on holding kernel state, and by every other measure it is
// converged - but it can no longer tell whether that state belongs to the guest it says it does, and it is
// withholding every renewal. CONVERGED there is a lie an operator acts on.
func TestEvidence_AnEvidenceOutageDegradesTheReportedPlane(t *testing.T) {
	p := liveWriter(newFakeTC())
	p.hasConverged = true
	p.lastConverged.ExpiresAt = timeNowPlusMinute()

	healthy := p.statusWithEvidence(evidenceHealth{Available: true, Leases: 4}, true)
	if healthy["state"] != shapingConverged || healthy["degraded"] != false {
		t.Fatalf("a healthy plane did not report converged: %+v", healthy)
	}

	out := p.statusWithEvidence(evidenceHealth{Fault: evidenceLeaseManagerUnavailable,
		Detail: "Kea is answering its control socket but has NO LEASE MANAGER"}, true)
	if out["state"] != shapingDegradedState || out["degraded"] != true {
		t.Fatalf("an evidence outage left the plane reporting %v (degraded=%v)", out["state"], out["degraded"])
	}
	oe, _ := out["ownership_evidence"].(map[string]any)
	if oe == nil || oe["available"] != false || oe["fault"] != evidenceLeaseManagerUnavailable {
		t.Fatalf("the fault is not visible in the status: %+v", out["ownership_evidence"])
	}
	if s, _ := out["problem"].(string); !contains(s, evidenceLeaseManagerUnavailable) {
		t.Fatalf("problem = %q, want it to name the evidence fault", s)
	}
}

// A DARK appliance has no plane to degrade, and an idle Kea on a box that has never been configured must not
// be reported as an enforcement fault.
func TestEvidence_ADarkApplianceIsNotDegradedByIt(t *testing.T) {
	p := &phase3Shaping{mode: phase3Mode{Active: false}}
	out := p.statusWithEvidence(evidenceHealth{Fault: evidenceLeaseManagerUnavailable}, true)
	if out["degraded"] != false || out["state"] != shapingDark {
		t.Fatalf("a dark appliance was degraded by DHCP: %+v", out)
	}
}

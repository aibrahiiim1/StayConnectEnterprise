package main

// A zero-byte kea-leases4.csv.pid leaves Kea answering status-get with no lease manager. The three
// conditions (lease manager gone, other query failure, on-disk cause) need different operator responses.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type failingLeases struct{ err error }

func (f failingLeases) Leases() ([]KeaLease, error) { return nil, f.err }

func timeNowPlusMinute() time.Time { return time.Now().Add(time.Minute) }

type staticFiles struct {
	path string
	err  error
}

func (s staticFiles) LeaseFile() (string, error) { return s.path, s.err }

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

// A vanished socket is an ordinary outage, distinct from a running Kea with no lease manager.
func TestEvidence_AnyOtherQueryFailureIsNotMistakenForIt(t *testing.T) {
	h := probeEvidence(context.Background(),
		failingLeases{err: errors.New("dial unix /run/kea/kea4-ctrl-socket: connect: no such file or directory")}, nil)

	if h.Available || h.Fault != evidenceQueryFailed {
		t.Fatalf("fault = %q available=%v, want %q", h.Fault, h.Available, evidenceQueryFailed)
	}
}

// When the query fails and the PID companion is empty, the on-disk cause outranks the symptom.
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
	// The probe must not repair (delete) the evidence.
	if _, err := os.Stat(leaseFile + ".pid"); err != nil {
		t.Fatalf("the probe removed the PID file: %v", err)
	}
	if _, err := os.Stat(leaseFile); err != nil {
		t.Fatalf("the probe touched the lease file: %v", err)
	}
}

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

func TestEvidence_NoSourceIsReportedAsAConfigurationFault(t *testing.T) {
	h := probeEvidence(context.Background(), nil, nil)
	if h.Available || h.Fault != evidenceNotConfigured {
		t.Fatalf("fault = %q, want %q", h.Fault, evidenceNotConfigured)
	}
}

// The plane must not report converged while its ownership authority is down.
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

func TestEvidence_ADarkApplianceIsNotDegradedByIt(t *testing.T) {
	p := &phase3Shaping{mode: phase3Mode{Active: false}}
	out := p.statusWithEvidence(evidenceHealth{Fault: evidenceLeaseManagerUnavailable}, true)
	if out["degraded"] != false || out["state"] != shapingDark {
		t.Fatalf("a dark appliance was degraded by DHCP: %+v", out)
	}
}

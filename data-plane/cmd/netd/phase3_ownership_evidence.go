package main

// THE ENFORCEMENT PLANE IS ONLY AS HONEST AS ITS DHCP EVIDENCE.
//
// Address ownership is what stops the appliance authorizing an address after the device that held it walked
// away. It is decided from Kea's leases, so DHCP is no longer merely the thing that hands out addresses: it is
// a SAFETY AUTHORITY, and its absence is a safety condition rather than a networking inconvenience.
//
// This is not a hypothetical. On the PRE-LIVE appliance, 2026-08-31: /var/lib/kea/kea-leases4.csv.pid was zero
// bytes, memfile refused to open its database while it could not read a PID from that file, and kea-dhcp4 went
// on running - answering status-get perfectly, serving the control socket, reporting healthy - with NO LEASE
// MANAGER AT ALL. Every lease command returned "no current lease manager is available". The server had issued
// no lease for two days. Nothing anywhere said so. The old health surface asked Kea only whether it answered
// status-get, which it did, cheerfully, throughout.
//
// So health now asks the question the ownership rule actually depends on: CAN WE READ THE LEASES? And it asks
// it by reading them, because a control socket that answers one command is no evidence that it answers the one
// that matters.
//
// WHAT THIS DELIBERATELY DOES NOT DO: it does not repair anything. It does not delete the PID file, restart
// Kea, compact the lease file or "fix" any DHCP state. A process that silently repairs the authority it is
// judging destroys the evidence of what went wrong and hides a recurring fault behind a self-healing loop. The
// zero-byte file on .25 had been there for three days and was worth finding. This surfaces the fault, names
// what it saw, and leaves the state exactly as it is for an operator to look at.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// Evidence fault codes. They are stable strings because operators, the health surface, the deployment check
// and the runbook all name the same condition, and a renamed code is a broken runbook.
const (
	// evidenceOK - leases were read.
	evidenceOK = ""
	// evidenceNotConfigured - this netd has no lease source at all. Distinct from a broken one: a build or a
	// wiring mistake, not a DHCP failure.
	evidenceNotConfigured = "LEASE_SOURCE_NOT_CONFIGURED"
	// evidenceLeaseManagerUnavailable - the exact .25 condition. Kea is up and talking; its lease manager is
	// gone. Ownership cannot be decided and DHCP is very probably not serving guests either.
	evidenceLeaseManagerUnavailable = "LEASE_MANAGER_UNAVAILABLE"
	// evidenceQueryFailed - the lease query failed for any other reason: socket gone, timeout, malformed reply.
	evidenceQueryFailed = "LEASE_QUERY_FAILED"
	// evidenceMemfileUnusable - the shipped memfile backing state cannot be opened. Found by inspection rather
	// than by asking Kea, so it is reportable even while a still-running server pretends everything is fine.
	evidenceMemfileUnusable = "MEMFILE_STATE_UNUSABLE"
)

// evidenceHealth is what the applier knows about its ownership authority.
type evidenceHealth struct {
	Available bool      `json:"available"`
	Fault     string    `json:"fault,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	Leases    int       `json:"leases"`
	CheckedAt time.Time `json:"-"`
}

// leaseFileInspector reports the memfile paths Kea was configured with. Separate from leaseSource because the
// two answer different questions - "what does Kea say" versus "what is on disk" - and the whole point of the
// .25 failure is that those two answers disagreed for three days.
type leaseFileInspector interface {
	LeaseFile() (string, error)
}

// probeEvidence asks the ownership authority whether it can still be consulted.
//
// ORDER MATTERS. The lease query comes first because it is the operation ownership actually performs: if it
// works, evidence is available and no amount of odd-looking state on disk changes that. Only when it fails do
// we look at the backing file, and then only to say something more useful than "it failed" - the difference
// between "Kea is down" and "Kea is up but its lease database never opened, and here is the artifact that
// stopped it" is the difference between a five-minute fix and the three days this took.
func probeEvidence(ctx context.Context, src leaseSource, files leaseFileInspector) evidenceHealth {
	h := evidenceHealth{CheckedAt: time.Now()}
	if src == nil {
		h.Fault = evidenceNotConfigured
		h.Detail = "netd has no DHCP lease source, so address ownership cannot be verified for any session"
		return h
	}
	leases, err := src.Leases()
	if err == nil {
		h.Available = true
		h.Leases = len(leases)
		return h
	}

	h.Fault, h.Detail = classifyEvidenceError(err)
	if note := inspectMemfile(files); note != "" {
		// The on-disk cause outranks the symptom: an operator needs the artifact, not the error text.
		h.Fault = evidenceMemfileUnusable
		h.Detail = h.Detail + "; " + note
	}
	return h
}

// classifyEvidenceError separates the condition seen on .25 from every other reason a query can fail.
func classifyEvidenceError(err error) (string, string) {
	msg := err.Error()
	if strings.Contains(strings.ToLower(msg), "no current lease manager") {
		return evidenceLeaseManagerUnavailable,
			"Kea is answering its control socket but has NO LEASE MANAGER: it cannot serve DHCP and cannot " +
				"answer ownership questions (" + msg + ")"
	}
	return evidenceQueryFailed, "the DHCP lease query failed: " + msg
}

// inspectMemfile looks for a memfile state that cannot be opened, and names the artifact responsible.
//
// The one condition that has actually happened is the lease-file PID companion: memfile writes <leasefile>.pid
// for its cleanup process and REFUSES TO OPEN ITS DATABASE if that file exists but yields no PID. A zero-byte
// one is therefore fatal to DHCP while being completely invisible to anything that only asks whether the
// service is running.
func inspectMemfile(files leaseFileInspector) string {
	if files == nil {
		return ""
	}
	path, err := files.LeaseFile()
	if err != nil || strings.TrimSpace(path) == "" {
		return ""
	}
	pid := path + ".pid"
	st, err := os.Stat(pid)
	if err != nil {
		return "" // absent is the normal, healthy case: nothing is holding the lease file
	}
	if st.Size() == 0 {
		return fmt.Sprintf("%s exists but is EMPTY - memfile refuses to open %s while it cannot read a PID "+
			"from it, which leaves Kea running with no lease manager. NOT repaired automatically: preserved "+
			"for inspection", pid, path)
	}
	if _, err := os.ReadFile(pid); err != nil {
		return fmt.Sprintf("%s cannot be read (%v) - memfile refuses to open %s without it. NOT repaired "+
			"automatically: preserved for inspection", pid, err, path)
	}
	return ""
}

// evidenceStatus is the health view, refreshed on demand rather than cached.
//
// Refreshing here matters: if acctd stops submitting plans, no pass runs, and a cached result would report the
// evidence as it was when the plane was last busy. An operator asking about health is asking about NOW.
func (p *phase3Shaping) evidenceStatus(ctx context.Context) evidenceHealth {
	return probeEvidence(ctx, p.owner.src, p.leaseFiles)
}

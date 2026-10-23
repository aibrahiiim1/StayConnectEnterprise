package main

// Address ownership is decided from Kea's leases, so DHCP is a safety authority: health asks whether the
// leases can actually be read, because Kea can answer status-get with no lease manager (a zero-byte
// kea-leases4.csv.pid does this). This never repairs DHCP state: self-repair would destroy the evidence.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// Fault codes are stable: operators, health, the deployment check and the runbook name them.
const (
	evidenceOK = ""
	// No lease source wired: a build or wiring mistake, not a DHCP failure.
	evidenceNotConfigured = "LEASE_SOURCE_NOT_CONFIGURED"
	// Kea answers its control socket but its lease manager is gone.
	evidenceLeaseManagerUnavailable = "LEASE_MANAGER_UNAVAILABLE"
	evidenceQueryFailed             = "LEASE_QUERY_FAILED"
	// Found by inspecting the disk, so reportable even while a running Kea claims to be fine.
	evidenceMemfileUnusable = "MEMFILE_STATE_UNUSABLE"
)

type evidenceHealth struct {
	Available bool      `json:"available"`
	Fault     string    `json:"fault,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	Leases    int       `json:"leases"`
	CheckedAt time.Time `json:"-"`
}

// leaseFileInspector is separate from leaseSource: "what is on disk" can disagree with "what Kea says".
type leaseFileInspector interface {
	LeaseFile() (string, error)
}

// The lease query runs first because it is what ownership actually does; the backing file is inspected only
// after a failure, to name the cause.
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
		h.Fault = evidenceMemfileUnusable
		h.Detail = h.Detail + "; " + note
	}
	return h
}

func classifyEvidenceError(err error) (string, string) {
	msg := err.Error()
	if strings.Contains(strings.ToLower(msg), "no current lease manager") {
		return evidenceLeaseManagerUnavailable,
			"Kea is answering its control socket but has NO LEASE MANAGER: it cannot serve DHCP and cannot " +
				"answer ownership questions (" + msg + ")"
	}
	return evidenceQueryFailed, "the DHCP lease query failed: " + msg
}

// inspectMemfile: Kea's memfile writes <leasefile>.pid and refuses to open its database if that file exists
// but yields no PID, which is invisible to a mere is-it-running check.
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
		return "" // absent is the healthy case
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

// Refreshed on demand, not cached: if acctd stops submitting plans no pass runs and a cache would go stale.
func (p *phase3Shaping) evidenceStatus(ctx context.Context) evidenceHealth {
	return probeEvidence(ctx, p.owner.src, p.leaseFiles)
}

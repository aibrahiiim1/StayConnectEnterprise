package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
)

// assignmentStore is the appliance-local source of truth for tenant/site.
func (s *server) assignmentStore() *assignment.Store {
	return &assignment.Store{Dir: envOr("SCD_ASSIGNMENT_DIR", "/etc/stayconnect/assignment")}
}

// cloudHTTPClient is the plain-HTTPS client used before an mTLS client cert
// exists. Central's server CA is installed in the appliance trust store, so the
// default roots verify it (same approach as the license fetcher).
func (s *server) cloudHTTPClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second}
}

// startAssignmentAgent polls Central for this appliance's signed assignment,
// verifies it (vendor signature + appliance binding + monotonic version),
// persists it atomically, repoints local guest-network ownership, and — when the
// operational tenant/site changes — re-execs scd so every subsystem picks up the
// new assignment. This is the ONLY channel by which an appliance adopts a
// tenant/site; there is no env/identity hard-wiring.
func (s *server) startAssignmentAgent(ctx context.Context, ctrlBase string) {
	raw, err := os.ReadFile(envOr("SCD_VENDOR_PUB", "/etc/stayconnect/vendor-license.pub"))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		slog.Warn("assignment: vendor public key unavailable — assignment agent disabled")
		return
	}
	pub := ed25519.PublicKey(raw)
	store := s.assignmentStore()

	// currentVersion = the version already applied on disk (0 if none).
	currentVersion := int64(0)
	if d, e := store.Load(); e == nil && d != nil {
		currentVersion = d.Version
	}
	slog.Info("assignment: agent started", "appliance_id", s.applID, "serial", s.serial,
		"identity_fpr", s.identityKeyFpr, "applied_version", currentVersion, "ctrl_base", ctrlBase)

	poll := func() {
		doc, ok := s.fetchAssignment(ctx, ctrlBase)
		if !ok || doc == nil {
			return
		}
		reason := assignment.AcceptFor(pub, doc, s.applID, s.serial, s.identityKeyFpr, currentVersion, time.Now())
		if reason != "" {
			// Not newer / not for us / bad signature — ignore quietly (a same or
			// older version is the steady state between reassignments).
			if doc.Version > currentVersion {
				slog.Warn("assignment: rejected", "reason", reason, "version", doc.Version)
			}
			return
		}
		if err := store.Save(doc); err != nil {
			slog.Error("assignment: persist failed", "err", err)
			return
		}
		slog.Info("assignment: applied signed assignment", "version", doc.Version, "state", doc.State,
			"tenant_id", doc.TenantID, "site_id", doc.SiteID)
		s.repointGuestNetworks(ctx, doc)
		prevVersion := currentVersion
		currentVersion = doc.Version

		// Decide whether the operational identity changed enough to re-exec.
		operationalChanged := doc.State != assignment.StateAssigned ||
			doc.TenantID != s.tenID || doc.SiteID != s.siteID
		if prevVersion == 0 || operationalChanged {
			slog.Info("assignment: re-executing scd to adopt new assignment", "version", doc.Version)
			s.reexec()
		}
	}

	poll() // apply immediately on boot if a newer assignment is waiting
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				poll()
			}
		}
	}()
}

// fetchAssignment GETs the current signed assignment over the mTLS transport
// (falling back to the legacy signed-JWT base when mTLS isn't ready).
func (s *server) fetchAssignment(ctx context.Context, ctrlBase string) (*assignment.Document, bool) {
	if s.idPriv == nil || s.applID == "" {
		return nil, false
	}
	// Prefer the mTLS transport; fall back to the license-fetch client (which
	// already trusts Central's server CA) over the signed-JWT channel on :443.
	cl := s.cloudHTTPClient()
	base := ctrlBase
	if s.certMgr != nil {
		if c, b, ready := s.certMgr.Transport(); ready {
			cl, base = c, b
		}
	}
	if base == "" {
		return nil, false
	}
	tok, err := applianceauth.SignRequest(s.idPriv, s.applID, http.MethodGet, "/v1/appliance/assignment", nil)
	if err != nil {
		slog.Warn("assignment: sign request failed", "err", err)
		return nil, false
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/appliance/assignment", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := cl.Do(req)
	if err != nil {
		slog.Warn("assignment: fetch failed", "base", base, "err", err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil, false // no assignment issued yet — normal while awaiting
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		slog.Warn("assignment: fetch non-200", "status", resp.StatusCode, "base", base, "body", string(b))
		return nil, false
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var doc assignment.Document
	if json.Unmarshal(body, &doc) != nil {
		slog.Warn("assignment: unparseable document")
		return nil, false
	}
	return &doc, true
}

// repointGuestNetworks updates every appliance-local row that carries a Central
// tenant/site UUID so it reflects the CURRENT assignment — never the old one.
// These columns are stored-but-not-read on the edge, so this is an ownership /
// consistency correction (proves zero stale UUIDs after onboarding); it never
// touches the WAN/LAN/management network. Best-effort, in one transaction.
func (s *server) repointGuestNetworks(ctx context.Context, doc *assignment.Document) {
	if s.db == nil || doc.State != assignment.StateAssigned || doc.TenantID == "" || doc.SiteID == "" {
		return
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	// The single site/tenant mirror rows + guest_networks ownership columns.
	_, _ = tx.Exec(ctx, `UPDATE guest_networks SET tenant_id=$1, site_id=$2`, doc.TenantID, doc.SiteID)
	if err := tx.Commit(ctx); err != nil {
		slog.Warn("assignment: guest-network repoint failed", "err", err)
		return
	}
	slog.Info("assignment: guest-network ownership repointed", "tenant_id", doc.TenantID, "site_id", doc.SiteID)
}

// reexec replaces the current process image with a fresh scd, so all subsystems
// re-read the newly-applied assignment. systemd keeps the same PID (this is not
// a crash), satisfying "restart only as part of the automated application flow".
func (s *server) reexec() {
	exe, err := os.Executable()
	if err != nil {
		slog.Error("assignment: cannot locate own binary for re-exec; exiting for supervisor restart", "err", err)
		os.Exit(3)
	}
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		slog.Error("assignment: re-exec failed; exiting for supervisor restart", "err", err)
		os.Exit(3)
	}
}

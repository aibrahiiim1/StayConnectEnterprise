package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/identity"
)

// assignmentStore is the appliance-local source of truth for tenant/site.
func (s *server) assignmentStore() *assignment.Store {
	return &assignment.Store{Dir: envOr("SCD_ASSIGNMENT_DIR", "/etc/stayconnect/assignment")}
}

func assignmentTrustPath() string {
	return envOr("SCD_ASSIGNMENT_TRUST", "/etc/stayconnect/assignment-trust.json")
}

func assignmentRegistryPath() string {
	return envOr("SCD_ASSIGNMENT_REGISTRY", "/etc/stayconnect/assignment/registry.json")
}
func assignmentRegistryRootPath() string {
	return envOr("SCD_ASSIGNMENT_REGISTRY_ROOT", "/etc/stayconnect/assignment-registry-root.pub")
}

// registryStore is the durable store for the SIGNED trust registry, anchored by
// the manufacture-time registry root public key. nil if no root anchor exists yet
// (falls back to the legacy plain trust file during rollout).
func registryStoreOnDisk() *assignment.RegistryStore {
	rootPub, err := assignment.LoadRootPub(assignmentRegistryRootPath())
	if err != nil {
		return nil
	}
	return &assignment.RegistryStore{Path: assignmentRegistryPath(), RootPub: rootPub}
}

// currentRegistryOnDisk returns the trusted assignment-key registry: preferring
// the verified SIGNED registry (last-known-good survives a Central outage),
// falling back to the legacy plain trust file only where no signed registry
// exists yet.
func currentRegistryOnDisk() (*assignment.Registry, string) {
	if rs := registryStoreOnDisk(); rs != nil {
		// Best last-known-good: current if it verifies, else the previous verified
		// registry — this is what keeps a hotel running through a Central outage.
		if reg := rs.Trusted(); reg != nil {
			return reg, "signed"
		}
		// Root anchor present AND a signed-registry file already exists on disk = the
		// signed regime is in force. A file that no longer verifies is tampering or
		// corruption; REFUSE to downgrade to the unauthenticated legacy plain trust
		// file (that would let anyone who can write the file authorise a rogue key).
		if rs.FileExists() {
			slog.Error("assignment: on-disk signed registry failed verification — refusing to downgrade to the unsigned trust file")
			return nil, "none"
		}
	}
	// Pre-rollout only: no signed registry has ever been persisted yet.
	reg, err := assignment.LoadRegistry(assignmentTrustPath())
	if err != nil {
		return nil, "none"
	}
	return reg, "legacy-plain"
}

// verifiedAssignment loads the persisted assignment and RE-VERIFIES it on boot
// against the local trust registry: the signature must come from an ACTIVE
// dedicated assignment-signing key, and the document must be bound to THIS
// appliance. A document signed by a retired key — or by the license / command /
// update / CA key, none of which are in the registry — is refused, and the
// appliance falls back to awaiting-assignment rather than operating on an
// unverifiable identity.
//
// Returns (tenantID, siteID, state, version); tenant/site are non-empty only for a
// verified 'assigned' document.
func verifiedAssignment(store *assignment.Store, ident *identity.Identity) (string, string, string, int64) {
	rec, err := store.Load()
	if err != nil || rec == nil || rec.Current == nil {
		return "", "", "", 0
	}
	d := rec.Current
	// Prefer the verified SIGNED registry (last-known-good survives outages);
	// fall back to the legacy plain trust file only where none exists yet.
	reg, src := currentRegistryOnDisk()
	if reg == nil {
		slog.Error("assignment: no trusted registry — refusing to trust the persisted assignment")
		return "", "", "", 0
	}
	if src != "signed" {
		slog.Warn("assignment: verifying against legacy plain trust file (no signed registry yet)")
	}
	fpr := ""
	if raw, e := base64.RawStdEncoding.DecodeString(ident.PublicKeyB64); e == nil && len(raw) == ed25519.PublicKeySize {
		fpr = applianceauth.KeyID(ed25519.PublicKey(raw))
	}
	// haveVersion = d.Version-1 so the persisted document itself is admissible.
	// A verify_only signer still verifies here — that is what lets an appliance
	// holding an older-key assignment reboot cleanly mid-rotation.
	if reason := assignment.AcceptForRegistry(reg, d, ident.ApplianceID, ident.Serial, fpr, d.Version-1, time.Now()); reason != "" {
		slog.Error("assignment: persisted assignment FAILED verification — ignoring it",
			"reason", reason, "signer_key_id", d.SignerKeyID, "version", d.Version)
		return "", "", "", 0
	}
	// Expiry is NOT a de-authorisation: a stale assignment keeps the hotel running
	// through a Central outage. Warn, but keep operating on the last known good.
	if assignment.IsExpired(d, time.Now()) {
		slog.Warn("assignment: STALE (past expires_at, not refreshed) — retaining last-known-good tenant/site; guest operation continues",
			"version", d.Version, "expired_at", d.ExpiresAt, "stale_since", rec.StaleSince,
			"last_refresh_success", rec.LastRefreshSuccess)
	}
	if assignment.Grants(d.State) {
		return d.TenantID, d.SiteID, d.State, d.Version
	}
	return "", "", d.State, d.Version
}

// cloudHTTPClient is the plain-HTTPS client used before an mTLS client cert
// exists. Central's server CA is installed in the appliance trust store, so the
// default roots verify it (same approach as the license fetcher).
func (s *server) cloudHTTPClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second}
}

// assignmentStatus surfaces the durable assignment record for the setup wizard and
// the Platform. `status: stale` means the document is past expires_at and Central
// has not refreshed it — the appliance KEEPS operating on it; this is a warning for
// operators, not a loss of authority.
func (s *server) assignmentStatus() map[string]any {
	store := s.assignmentStore()
	rec, err := store.Load()
	if err != nil || rec == nil || rec.Current == nil {
		return map[string]any{"status": assignment.StatusNone, "assigned": false}
	}
	d := rec.Current
	out := map[string]any{
		"status":               store.Status(), // current | stale
		"assigned":             assignment.Grants(d.State),
		"lifecycle_state":      d.State,
		"version":              rec.Version,
		"signer_key_id":        rec.SignerKeyID,
		"tenant_id":            d.TenantID,
		"site_id":              d.SiteID,
		"tenant_name":          d.TenantName,
		"site_name":            d.SiteName,
		"adopted_at":           rec.AdoptedAt,
		"last_refresh_success": rec.LastRefreshSuccess,
		"last_refresh_attempt": rec.LastRefreshAttempt,
		"stale_since":          rec.StaleSince,
		"expires_at":           d.ExpiresAt,
		"expired":              assignment.IsExpired(d, time.Now()),
	}
	if rec.Previous != nil {
		out["previous_version"] = rec.Previous.Version
	}
	return out
}

// startAssignmentAgent polls Central for this appliance's signed assignment,
// verifies it (vendor signature + appliance binding + monotonic version),
// persists it atomically, repoints local guest-network ownership, and — when the
// operational tenant/site changes — re-execs scd so every subsystem picks up the
// new assignment. This is the ONLY channel by which an appliance adopts a
// tenant/site; there is no env/identity hard-wiring.
func (s *server) startAssignmentAgent(ctx context.Context, ctrlBase string) {
	// The assignment trust registry is the appliance's LOCAL list of assignment-
	// signing public keys. It is re-read on every poll so a rotated registry takes
	// effect without a restart. It holds ONLY assignment keys — the license,
	// command, update, CA and auth-callout keys are absent, so a document signed by
	// any of those is rejected as an unknown signer.
	if reg, _ := currentRegistryOnDisk(); reg == nil && registryStoreOnDisk() == nil {
		slog.Warn("assignment: no trusted registry and no root anchor — assignment agent disabled")
		return
	}
	store := s.assignmentStore()

	// currentVersion = the version already applied on disk (0 if none).
	currentVersion := int64(0)
	if rec, e := store.Load(); e == nil && rec != nil {
		currentVersion = rec.Version
	}
	slog.Info("assignment: agent started", "appliance_id", s.applID, "serial", s.serial,
		"identity_fpr", s.identityKeyFpr, "applied_version", currentVersion,
		"ctrl_base", ctrlBase, "status", store.Status())

	poll := func() {
		// Refresh the SIGNED trust registry first (verify + persist, rollback-safe).
		// A failure keeps the last-known-good registry — verification continues offline.
		s.refreshSignedRegistry(ctx)

		doc, ok := s.fetchAssignment(ctx, ctrlBase)
		if !ok || doc == nil {
			// Central unreachable, or nothing to hand us. Record the attempt so an
			// operator can see refresh is failing. This NEVER downgrades or clears
			// the assignment — an unreachable Central must not unassign a hotel.
			store.NoteRefresh(false)
			return
		}
		// Central answered: refresh succeeded, so we are no longer stale even if the
		// document we hold is the same one.
		store.NoteRefresh(true)

		reg, _ := currentRegistryOnDisk()
		if reg == nil {
			slog.Error("assignment: no trusted registry — cannot verify")
			return
		}
		reason := assignment.AcceptForRegistry(reg, doc, s.applID, s.serial, s.identityKeyFpr, currentVersion, time.Now())
		if reason != "" {
			// Not newer / not for us / bad signature — ignore quietly (a same or
			// older version is the steady state between reassignments).
			if doc.Version > currentVersion {
				slog.Warn("assignment: rejected", "reason", reason, "version", doc.Version)
			}
			return
		}
		// Adopt: rotates the outgoing document into `previous` (rollback-safe) and
		// resets staleness.
		if err := store.Adopt(doc); err != nil {
			slog.Error("assignment: persist failed", "err", err)
			return
		}
		slog.Info("assignment: applied signed assignment", "version", doc.Version, "state", doc.State,
			"signer_key_id", doc.SignerKeyID, "tenant_id", doc.TenantID, "site_id", doc.SiteID)
		s.repointGuestNetworks(ctx, doc)
		prevVersion := currentVersion
		currentVersion = doc.Version

		// TERMINAL adoption: the box has cleared its authority. Send Central a signed
		// acknowledgment BEFORE re-exec so Phase 2 (cert + NATS shutdown) can proceed.
		if assignment.Clears(doc.State) {
			s.sendTerminalAck(ctx, doc)
		}

		// Re-exec when the operational identity actually changes (including an
		// explicit unassign/revoke/decommission, which removes tenant/site).
		operationalChanged := !assignment.Grants(doc.State) ||
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

// mtlsTransport returns the mTLS client + base, or (nil,"",false) if a client
// certificate is not yet established. The assignment channel is mTLS-ONLY: there
// is no JWT-over-:443 fallback, so a document can only reach a box holding a
// valid client certificate.
func (s *server) mtlsTransport() (*http.Client, string, bool) {
	if s.certMgr == nil {
		return nil, "", false
	}
	return s.certMgr.Transport()
}

// fetchAssignment GETs the current signed assignment over the mTLS transport ONLY.
// Identity is carried entirely by the client certificate — Central authenticates
// this request from the cert alone, so NO appliance JWT/bearer is attached.
func (s *server) fetchAssignment(ctx context.Context, _ string) (*assignment.Document, bool) {
	if s.applID == "" {
		return nil, false
	}
	cl, base, ready := s.mtlsTransport()
	if !ready || base == "" {
		return nil, false // mTLS not ready — assignment is mTLS-only, no fallback
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/appliance/assignment", nil)
	resp, err := cl.Do(req)
	if err != nil {
		slog.Warn("assignment: fetch failed", "base", base, "err", err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil, false
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		slog.Warn("assignment: fetch non-200", "status", resp.StatusCode, "body", string(b))
		return nil, false
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var doc assignment.Document
	if json.Unmarshal(body, &doc) != nil {
		return nil, false
	}
	return &doc, true
}

// refreshSignedRegistry fetches the current signed trust registry over mTLS,
// verifies it against the baked-in root anchor and its version rules, and
// persists it (keeping the previous one). A failure keeps the last-known-good
// registry in force — verification of assignments continues during a Central
// outage. Rejections are logged (and count as an audit signal).
func (s *server) refreshSignedRegistry(ctx context.Context) {
	rs := registryStoreOnDisk()
	if rs == nil || s.idPriv == nil || s.applID == "" {
		return
	}
	cl, base, ready := s.mtlsTransport()
	if !ready || base == "" {
		return
	}
	tok, err := applianceauth.SignRequest(s.idPriv, s.applID, http.MethodGet, "/v1/appliance/assignment-registry", nil)
	if err != nil {
		return
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/appliance/assignment-registry", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := cl.Do(req)
	if err != nil {
		return // outage — keep last-known-good
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var sr assignment.SignedRegistry
	if json.Unmarshal(body, &sr) != nil {
		return
	}
	adopted, reason := rs.Adopt(&sr, time.Now())
	if reason != "" && sr.RegistryVersion > rs.CurrentVersion() {
		slog.Warn("assignment: signed registry REJECTED (last-known-good retained)",
			"reason", reason, "offered_version", sr.RegistryVersion, "current_version", rs.CurrentVersion())
		return
	}
	if adopted {
		slog.Info("assignment: adopted signed trust registry", "registry_version", sr.RegistryVersion,
			"keys", len(sr.Keys), "signer_key_id", sr.SignerKeyID)
	}
}

// sendTerminalAck signs and delivers the appliance's acknowledgment that it has
// adopted a TERMINAL assignment (over mTLS). This is what authorises Central to
// run Phase 2 (certificate + NATS shutdown).
func (s *server) sendTerminalAck(ctx context.Context, doc *assignment.Document) {
	if s.idPriv == nil || s.applID == "" {
		return
	}
	cl, base, ready := s.mtlsTransport()
	if !ready || base == "" {
		slog.Warn("assignment: terminal adopted but mTLS not ready to acknowledge; Central will retry / time out")
		return
	}
	ack := &assignment.Ack{
		ApplianceID: s.applID, Version: doc.Version, TerminalState: doc.State,
		Fingerprint: assignment.DocFingerprint(doc), AdoptedAt: time.Now().Unix(),
	}
	assignment.SignAck(s.idPriv, ack)
	body, _ := json.Marshal(ack)
	tok, err := applianceauth.SignRequest(s.idPriv, s.applID, http.MethodPost, "/v1/appliance/assignment/ack", body)
	if err != nil {
		return
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/appliance/assignment/ack", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		slog.Warn("assignment: terminal ack send failed", "err", err)
		return
	}
	defer resp.Body.Close()
	slog.Info("assignment: sent terminal-adoption ack", "version", doc.Version, "state", doc.State,
		"http", resp.StatusCode)
}

// repointGuestNetworks updates every appliance-local row that carries a Central
// tenant/site UUID so it reflects the CURRENT assignment — never the old one.
// These columns are stored-but-not-read on the edge, so this is an ownership /
// consistency correction (proves zero stale UUIDs after onboarding); it never
// touches the WAN/LAN/management network. Best-effort, in one transaction.
func (s *server) repointGuestNetworks(ctx context.Context, doc *assignment.Document) {
	if s.db == nil || !assignment.Grants(doc.State) || doc.TenantID == "" || doc.SiteID == "" {
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

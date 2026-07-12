package main

import (
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
	reg, err := assignment.LoadRegistry(assignmentTrustPath())
	if err != nil {
		slog.Error("assignment: trust registry unavailable — refusing to trust the persisted assignment",
			"path", assignmentTrustPath(), "err", err)
		return "", "", "", 0
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
	trustPath := envOr("SCD_ASSIGNMENT_TRUST", "/etc/stayconnect/assignment-trust.json")
	if _, err := assignment.LoadRegistry(trustPath); err != nil {
		slog.Warn("assignment: trust registry unavailable — assignment agent disabled",
			"path", trustPath, "err", err)
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
		"ctrl_base", ctrlBase, "trust_registry", trustPath, "status", store.Status())

	poll := func() {
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

		reg, err := assignment.LoadRegistry(trustPath) // re-read: supports live rotation
		if err != nil {
			slog.Error("assignment: trust registry unreadable", "err", err)
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

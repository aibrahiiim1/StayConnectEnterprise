package main

import (
	"context"
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

// registryStoreOnDisk returns the signed registry store; nil if no root anchor exists yet.
func registryStoreOnDisk() *assignment.RegistryStore {
	rootPub, err := assignment.LoadRootPub(assignmentRegistryRootPath())
	if err != nil {
		return nil
	}
	return &assignment.RegistryStore{Path: assignmentRegistryPath(), RootPub: rootPub}
}

// currentRegistryOnDisk prefers the verified signed registry (last-known-good survives a Central outage), else the legacy plain trust file.
func currentRegistryOnDisk() (*assignment.Registry, string) {
	return assignment.TrustedRegistry(assignmentPaths(), slog.Default())
}

// assignmentPaths is the single place scd resolves the on-disk assignment chain; the resolver lives in internal/assignment.
func assignmentPaths() assignment.Paths {
	return assignment.Paths{
		Dir:              envOr("SCD_ASSIGNMENT_DIR", "/etc/stayconnect/assignment"),
		RegistryPath:     assignmentRegistryPath(),
		RegistryRootPath: assignmentRegistryRootPath(),
		TrustPath:        assignmentTrustPath(),
	}
}

// verifiedAssignment re-verifies the persisted assignment against the local trust registry (active assignment-signing key, bound to THIS appliance).
// The Outcome says why there is no scope; a development build may fall back to its env tenant/site only when nothing is present.
func verifiedAssignment(_ *assignment.Store, ident *identity.Identity) assignment.Resolution {
	return assignment.Resolve(assignmentPaths(), assignment.ApplianceBinding{
		ApplianceID: ident.ApplianceID, Serial: ident.Serial, PublicKeyB64: ident.PublicKeyB64,
	}, time.Now(), slog.Default())
}

// startAssignmentAgent is the ONLY channel by which an appliance adopts a tenant/site; it re-execs scd when the operational identity changes.
func (s *server) startAssignmentAgent(ctx context.Context, ctrlBase string) {
	// The registry holds ONLY assignment keys, so a document signed by any other key is rejected; re-read every poll.
	if reg, _ := currentRegistryOnDisk(); reg == nil && registryStoreOnDisk() == nil {
		slog.Warn("assignment: no trusted registry and no root anchor — assignment agent disabled")
		return
	}
	store := s.assignmentStore()

	currentVersion := int64(0)
	if rec, e := store.Load(); e == nil && rec != nil {
		currentVersion = rec.Version
	}
	slog.Info("assignment: agent started", "appliance_id", s.applID, "serial", s.serial,
		"identity_fpr", s.identityKeyFpr, "applied_version", currentVersion,
		"ctrl_base", ctrlBase, "status", store.Status())

	poll := func() {
		// A refresh failure keeps the last-known-good registry.
		s.refreshSignedRegistry(ctx)

		doc, ok := s.fetchAssignment(ctx, ctrlBase)
		if !ok || doc == nil {
			// An unreachable Central must never unassign a hotel.
			store.NoteRefresh(false)
			return
		}
		store.NoteRefresh(true)

		reg, _ := currentRegistryOnDisk()
		if reg == nil {
			slog.Error("assignment: no trusted registry — cannot verify")
			return
		}
		reason := assignment.AcceptForRegistry(reg, doc, s.applID, s.serial, s.identityKeyFpr, currentVersion, time.Now())
		if reason != "" {
			if doc.Version > currentVersion {
				slog.Warn("assignment: rejected", "reason", reason, "version", doc.Version)
			}
			return
		}
		if err := store.Adopt(doc); err != nil {
			slog.Error("assignment: persist failed", "err", err)
			return
		}
		slog.Info("assignment: applied signed assignment", "version", doc.Version, "state", doc.State,
			"signer_key_id", doc.SignerKeyID, "tenant_id", doc.TenantID, "site_id", doc.SiteID)
		// Fetch the licence now rather than at the next tick.
		if s.lic != nil {
			s.lic.Kick()
		}
		s.repointGuestNetworks(ctx, doc)
		prevVersion := currentVersion
		currentVersion = doc.Version

		// Send the signed terminal ack BEFORE re-exec so credential revocation can proceed; ensureTerminalAck retries.
		if assignment.Clears(doc.State) {
			s.sendTerminalAck(ctx, doc)
			s.ensureTerminalAck(ctx)
		}

		operationalChanged := !assignment.Grants(doc.State) ||
			doc.TenantID != s.tenID || doc.SiteID != s.siteID
		if prevVersion == 0 || operationalChanged {
			slog.Info("assignment: re-executing scd to adopt new assignment", "version", doc.Version)
			s.reexec()
		}
	}

	// Retry a terminal ack Central never confirmed (interrupted by re-exec, restart or reboot).
	s.ensureTerminalAck(ctx)
	poll()
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				poll()
			case <-s.asgKick:
				poll()
			}
		}
	}()
}

// mtlsTransport: the assignment channel is mTLS-only, with no JWT fallback.
func (s *server) mtlsTransport() (*http.Client, string, bool) {
	if s.certMgr == nil {
		return nil, "", false
	}
	return s.certMgr.Transport()
}

// fetchAssignment authenticates by client certificate alone; no bearer is attached.
func (s *server) fetchAssignment(ctx context.Context, _ string) (*assignment.Document, bool) {
	if s.applID == "" {
		return nil, false
	}
	cl, base, ready := s.mtlsTransport()
	if !ready || base == "" {
		// No mTLS yet and no fallback. Log rarely: waiting for activation is normal, but silence hid a stuck activation.
		s.noMTLSLogged++
		if n := s.noMTLSLogged; n == 1 || n == 2 || n%20 == 0 {
			slog.Info("assignment: waiting for the mTLS client certificate before fetching the assignment",
				"polls_blocked", n,
				"note", "the certificate is issued when this appliance is activated in the control panel")
		}
		return nil, false
	}
	s.noMTLSLogged = 0
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/appliance/assignment", nil)
	resp, err := cl.Do(req)
	if err != nil {
		s.central.record(err, time.Now())
		slog.Warn("assignment: fetch failed", "base", base, "err", err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		s.central.record(nil, time.Now())
		return nil, false
	}
	if resp.StatusCode != http.StatusOK {
		s.central.record(&httpStatusErr{code: resp.StatusCode}, time.Now())
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			// Certificate refused: have the certificate agent check for revocation or reissue.
			select {
			case s.certReconcile <- struct{}{}:
			default:
			}
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		slog.Warn("assignment: fetch non-200", "status", resp.StatusCode, "body", string(b))
		return nil, false
	}
	s.central.record(nil, time.Now())
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var doc assignment.Document
	if json.Unmarshal(body, &doc) != nil {
		return nil, false
	}
	return &doc, true
}

// refreshSignedRegistry verifies against the root anchor and keeps the last-known-good registry on failure.
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
		return
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

// sendTerminalAck makes the first, synchronous delivery of the terminal acknowledgment (scd re-execs right after); runTerminalAck retries.
func (s *server) sendTerminalAck(ctx context.Context, doc *assignment.Document) {
	if s.terminalAckSettled(doc) {
		return
	}
	outcome, code := s.deliverTerminalAck(ctx, doc, time.Now())
	switch outcome {
	case ackConfirmed:
		s.settleTerminalAck(doc, "confirmed", code)
		slog.Info("assignment: sent terminal-adoption ack", "version", doc.Version, "state", doc.State, "http", code)
	case ackRefused:
		s.settleTerminalAck(doc, "refused", code)
		slog.Warn("assignment: Central refused the terminal-adoption ack", "version", doc.Version, "http", code)
	default:
		slog.Warn("assignment: terminal-adoption ack not confirmed yet; it will be retried until Central answers",
			"version", doc.Version, "http", code)
	}
}

// repointGuestNetworks restamps appliance-local rows carrying a Central tenant/site UUID with the current assignment; best-effort, one transaction.
func (s *server) repointGuestNetworks(ctx context.Context, doc *assignment.Document) {
	if s.db == nil || !assignment.Grants(doc.State) || doc.TenantID == "" || doc.SiteID == "" {
		return
	}
	// Mirror rows first: guest-domain tables FK to them.
	s.seedTenantSiteMirror(ctx, doc.TenantID, doc.SiteID, doc.TenantName, doc.SiteName)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	_, _ = tx.Exec(ctx, `UPDATE guest_networks SET tenant_id=$1, site_id=$2`, doc.TenantID, doc.SiteID)
	if err := tx.Commit(ctx); err != nil {
		slog.Warn("assignment: guest-network repoint failed", "err", err)
		return
	}
	slog.Info("assignment: guest-network ownership repointed", "tenant_id", doc.TenantID, "site_id", doc.SiteID)
}

// seedTenantSiteMirror upserts the local tenant/site mirror rows that guest-domain tables FK to; idempotent.
func (s *server) seedTenantSiteMirror(ctx context.Context, tenantID, siteID, tenantName, siteName string) {
	if s.db == nil || tenantID == "" || siteID == "" {
		return
	}
	if tenantName == "" {
		tenantName = tenantID
	}
	if siteName == "" {
		siteName = siteID
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
        INSERT INTO tenants (id, slug, name)
        VALUES ($1, $2, $3)
        ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, updated_at = now()
    `, tenantID, tenantID, tenantName); err != nil {
		slog.Warn("assignment: tenant mirror upsert failed", "err", err)
		return
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO sites (id, tenant_id, code, name, timezone)
        VALUES ($1, $2, $3, $4, 'UTC')
        ON CONFLICT (id) DO UPDATE SET tenant_id = EXCLUDED.tenant_id, name = EXCLUDED.name, updated_at = now()
    `, siteID, tenantID, siteID, siteName); err != nil {
		slog.Warn("assignment: site mirror upsert failed", "err", err)
		return
	}
	// Mirror row for the appliance: sessions.appliance_id FKs to appliances(id).
	if s.applID != "" {
		name := s.serial
		if name == "" {
			name = s.applID
		}
		serial := s.serial
		if serial == "" {
			serial = s.applID
		}
		if _, err := tx.Exec(ctx, `
        INSERT INTO appliances (id, tenant_id, site_id, serial, name, status)
        VALUES ($1, $2, $3, $4, $5, 'online')
        ON CONFLICT (id) DO UPDATE SET tenant_id = EXCLUDED.tenant_id, site_id = EXCLUDED.site_id, updated_at = now()
    `, s.applID, tenantID, siteID, serial, name); err != nil {
			slog.Warn("assignment: appliance mirror upsert failed", "err", err)
			return
		}
	}
	// Stamp local operators with the assigned tenant: p4_assert_financial_actor requires o.tenant_id = p_tenant, so
	// financial recovery could not be released otherwise. The tenant comes from the signed assignment; IS DISTINCT FROM follows a cross-tenant move.
	if _, err := tx.Exec(ctx, `
        UPDATE operators SET tenant_id = $1, updated_at = now()
         WHERE tenant_id IS DISTINCT FROM $1
    `, tenantID); err != nil {
		slog.Warn("assignment: operator tenant binding failed", "err", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Warn("assignment: tenant/site mirror commit failed", "err", err)
		return
	}
	slog.Info("assignment: tenant/site/appliance mirror upserted", "tenant_id", tenantID, "site_id", siteID, "appliance_id", s.applID)
}

// reexec replaces the process image (same PID) so all subsystems re-read the assignment.
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

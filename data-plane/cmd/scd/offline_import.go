package main

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/offline"
	lic "github.com/stayconnect/enterprise/license"
)

// setupOfflineImport imports an appliance-specific signed offline package: verifies signature, binding and expiry, enforces
// single use via a reboot-persistent ledger, installs the embedded license and records consumption for later reconciliation.
// Rejected packages mutate nothing.
func (s *server) setupOfflineImport(w http.ResponseWriter, r *http.Request) {
	var pkg offline.Package
	if err := json.NewDecoder(r.Body).Decode(&pkg); err != nil {
		httpErr(w, http.StatusBadRequest, "bad package")
		return
	}
	raw, err := os.ReadFile(envOr("SCD_VENDOR_PUB", "/etc/stayconnect/vendor-license.pub"))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		httpErr(w, http.StatusServiceUnavailable, "vendor public key unavailable")
		return
	}
	pub := ed25519.PublicKey(raw)
	mtlsFpr := ""
	if s.certMgr != nil {
		if st := s.certMgr.Status(); st != nil {
			if v, ok := st["cert_fingerprint"].(string); ok {
				mtlsFpr = v
			}
		}
	}
	if reason := offline.AcceptFor(pub, &pkg, s.applID, s.serial, s.identityKeyFpr, mtlsFpr, time.Now()); reason != "" {
		httpErr(w, http.StatusForbidden, "package rejected: "+reason)
		return
	}
	// AD-15(b): the package must name the customer and site of this appliance's signed assignment.
	if tenant, site, ok := s.assignedTenantSite(); ok && (pkg.TenantID != tenant || pkg.SiteID != site) {
		httpErr(w, http.StatusForbidden, "package rejected: it is for a different customer or site than this appliance's signed assignment")
		return
	}
	// AD-18(a): the single-use ledger row and the licence install succeed or fail together. The row is written in a
	// transaction that commits only after the install, so a package whose install fails (rollback protection, a licence
	// for another appliance) is not burned and can be retried or replaced.
	if s.db == nil {
		httpErr(w, http.StatusServiceUnavailable, "the single-use package ledger is unavailable (no database); try again when the database is up")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "ledger error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	// edge_offline_packages is declared by migration 0048 and must not be created here: it would diverge from the declared shape on a restored older dump.
	tag, err := tx.Exec(r.Context(),
		`INSERT INTO edge_offline_packages (package_id, nonce) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
		pkg.PackageID, pkg.Nonce)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "ledger error")
		return
	}
	if tag.RowsAffected() == 0 {
		httpErr(w, http.StatusConflict, "package already consumed (single-use)")
		return
	}
	// An old or replayed offline file is rejected by the anti-rollback store; nothing is recorded as consumed then.
	installed := false
	if s.lic != nil && len(pkg.LicenseEnvelope) > 0 && string(pkg.LicenseEnvelope) != "null" {
		if _, err := s.lic.Install(r.Context(), pkg.LicenseEnvelope); err != nil {
			if errors.Is(err, lic.ErrRollback) {
				writeJSON(w, http.StatusConflict, map[string]any{
					"error": "LICENSE_ROLLBACK_REJECTED", "package_id": pkg.PackageID,
					"detail": err.Error(),
				})
				return
			}
			httpErr(w, http.StatusBadRequest, "license install failed: "+err.Error())
			return
		}
		installed = true
	}
	if err := tx.Commit(r.Context()); err != nil {
		// The licence is installed; the ledger row was not recorded. Re-importing the same file is harmless: the
		// installed licence is the same version and the anti-rollback store accepts it idempotently.
		slog.Error("offline import: licence installed but the single-use ledger row could not be committed", "package_id", pkg.PackageID, "err", err)
		httpErr(w, http.StatusInternalServerError, "ledger error after install; the licence is installed, import the same file again to record it")
		return
	}
	// Reconcile with Central (idempotent); offlineReconcileLoop (central_retry.go) retries until Central confirms.
	s.kickOfflineReconcile()

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "activated", "package_id": pkg.PackageID, "license_installed": installed,
		"note": "single-use consumption recorded; reconciles with Central when online",
	})
}

// assignedTenantSite is the customer and site of this appliance's verified, granting signed assignment.
func (s *server) assignedTenantSite() (tenantID, siteID string, ok bool) {
	r, doc := s.resolveOwnAssignment(time.Now().UTC())
	if doc == nil || !r.Assigned() {
		return "", "", false
	}
	return doc.TenantID, doc.SiteID, true
}

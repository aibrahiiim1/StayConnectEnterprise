package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/data-plane/internal/offline"
	lic "github.com/stayconnect/enterprise/license"
)

// setupOfflineImport imports an appliance-specific signed offline activation
// package: it verifies the vendor signature + binding (id/serial/key
// fingerprints) + expiry, enforces SINGLE-USE via a reboot-persistent local
// ledger, installs the embedded signed license, and records consumption for
// later online reconciliation. Rejected packages mutate nothing.
func (s *server) setupOfflineImport(w http.ResponseWriter, r *http.Request) {
	var pkg offline.Package
	if err := json.NewDecoder(r.Body).Decode(&pkg); err != nil {
		httpErr(w, http.StatusBadRequest, "bad package")
		return
	}
	// Vendor public key (the appliance already trusts it for licenses).
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
	// Verify signature + binding + expiry for THIS appliance.
	if reason := offline.AcceptFor(pub, &pkg, s.applID, s.serial, s.identityKeyFpr, mtlsFpr, time.Now()); reason != "" {
		httpErr(w, http.StatusForbidden, "package rejected: "+reason)
		return
	}
	// Single-use: reboot-persistent ledger in the site DB.
	if s.db != nil {
		_, _ = s.db.Exec(r.Context(), `CREATE TABLE IF NOT EXISTS edge_offline_packages (
            package_id UUID PRIMARY KEY, nonce TEXT UNIQUE, consumed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
            reconciled_at TIMESTAMPTZ)`)
		tag, err := s.db.Exec(r.Context(),
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
	}
	// Install the embedded signed license (offline activation). A superseded/
	// replayed offline file is REJECTED by the anti-rollback store — the
	// consumption ledger entry above still stands (single-use), but no old
	// authority is restored.
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
	// Reconcile with Central (best-effort, idempotent). If offline now, a boot
	// reconcile retries later.
	go s.reconcileOfflinePackage(pkg.PackageID)

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "activated", "package_id": pkg.PackageID, "license_installed": installed,
		"note": "single-use consumption recorded; reconciles with Central when online",
	})
}

// reconcileOfflinePackage tells Central the package was consumed (idempotent).
// On success it stamps reconciled_at locally so boot-reconcile stops retrying.
func (s *server) reconcileOfflinePackage(packageID string) {
	if s.certMgr == nil || s.applID == "" {
		return
	}
	cl, base, ok := s.certMgr.Transport()
	if !ok {
		return // offline; boot-reconcile will retry
	}
	body := []byte(`{"package_id":"` + packageID + `"}`)
	tok, err := applianceauth.SignRequest(s.idPriv, s.applID, http.MethodPost, "/v1/appliance/offline-reconcile", body)
	if err != nil {
		return
	}
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/appliance/offline-reconcile", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
	if resp.StatusCode == 200 && s.db != nil {
		_, _ = s.db.Exec(context.Background(), `UPDATE edge_offline_packages SET reconciled_at=now() WHERE package_id=$1`, packageID)
	}
}

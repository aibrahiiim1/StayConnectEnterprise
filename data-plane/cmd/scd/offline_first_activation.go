package main

// Offline first activation, appliance half: GET /v1/central/offline-request emits the request (generating the local keypair
// if absent); POST /v1/central/offline-package verifies and applies the package Central returned (a first-activation package
// is recognised by shape; see centralOfflinePackage). Separate from the offline licence package (offline_import.go).
// Applying ends with the appliance either assigned and licensed or exactly as it started: the step most likely to fail runs
// first, the irreversible one last, and any failure after a durable write is undone. See applyActivationPackage.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/activation"
	"github.com/stayconnect/enterprise/data-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/hwid"
	lic "github.com/stayconnect/enterprise/license"
)

// activationRequestFile keeps the emitted request beside the identity so the answering package can be matched; a second request would invalidate the first.
func (s *server) activationRequestPath() string {
	return filepath.Join(envOr("SCD_IDENTITY_DIR", "/etc/stayconnect/identity"), "activation-request.json")
}

// activationJournalPath is the crash-recovery marker. Applying touches four durable places (ledger row, assignment file, trust
// bundle, licence store) with no cross-file transaction, and a half-activated box (assigned but unlicensed, or the reverse)
// looks finished. So the package is journalled before anything is written and removed only after everything; a journal present
// at boot means an interrupted activation, which recoverInterruptedActivation finishes or undoes. Both directions are safe
// because every step is idempotent (Adopt is version-checked, the licence store is anti-rollback, AdoptApplianceID refuses to repoint an identity).
func (s *server) activationJournalPath() string {
	return filepath.Join(envOr("SCD_IDENTITY_DIR", "/etc/stayconnect/identity"), "activation-in-progress.json")
}

// setupActivationRequest emits the request, generating the identity keypair locally if absent. The private half never
// leaves: the request carries the public key and a signature made with the private one.
func (s *server) setupActivationRequest(w http.ResponseWriter, r *http.Request) {
	if s.refuseWhileRemoved(w) {
		return
	}
	if s.applID != "" {
		httpErr(w, http.StatusConflict, "this appliance is already enrolled — first activation does not apply")
		return
	}
	if s.idStore == nil {
		httpErr(w, http.StatusServiceUnavailable, "identity store unavailable")
		return
	}
	id, err := s.idStore.EnsureLocalKeypair()
	if err != nil || id == nil {
		httpErr(w, http.StatusInternalServerError, "could not prepare an identity for this appliance")
		return
	}
	// The request says whose customer data this appliance still holds (held_customer.go); Central will not activate it for anyone else.
	held, err := heldCustomerID(r.Context(), s.db, envOr("SCD_ASSIGNMENT_DIR", "/etc/stayconnect/assignment"))
	if errors.Is(err, errHoldsSeveralCustomers) {
		httpErr(w, http.StatusConflict, err.Error())
		return
	} else if err != nil {
		httpErr(w, http.StatusServiceUnavailable, "could not read which customer's data this appliance holds; try again")
		return
	}
	// Reuse an outstanding request: a fresh nonce would invalidate the package the operator is carrying back.
	if raw, err := os.ReadFile(s.activationRequestPath()); err == nil {
		var prev activation.Request
		if json.Unmarshal(raw, &prev) == nil && activation.VerifyRequest(&prev) &&
			prev.PublicKey == id.PublicKeyB64 && prev.HoldsCustomerID == held {
			writeJSON(w, http.StatusOK, prev)
			return
		}
	}
	hw := hwid.Detect()
	req := &activation.Request{
		SchemaVersion: activation.SchemaVersion,
		RequestID:     offlineNonceHex(),
		Serial:        firstNonEmpty(hw.Serial, id.Serial, s.serial),
		PublicKey:     id.PublicKeyB64,
		WANMAC:        hw.WANMAC,
		LANMAC:        hw.LANMAC,
		HardwareFpr:   hw.Fingerprint,
		Hostname:      hw.Hostname,
		Model:         hw.Model,
		CreatedAt:     time.Now().Unix(),
		Nonce:         offlineNonceHex(),

		HoldsCustomerID: held,
	}
	activation.SignRequest(id.PrivateKey(), req)
	b, _ := json.MarshalIndent(req, "", "  ")
	if err := os.WriteFile(s.activationRequestPath(), b, 0o644); err != nil {
		httpErr(w, http.StatusInternalServerError, "could not record the activation request")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="activation-request-`+req.Serial+`.json"`)
	_, _ = w.Write(b)
}

func (s *server) setupActivationPackage(w http.ResponseWriter, r *http.Request) {
	var pkg activation.Package
	if err := json.NewDecoder(r.Body).Decode(&pkg); err != nil {
		httpErr(w, http.StatusBadRequest, "that file is not an activation package")
		return
	}
	if s.idStore == nil {
		httpErr(w, http.StatusServiceUnavailable, "activation stores unavailable")
		return
	}
	raw, err := os.ReadFile(s.activationRequestPath())
	if err != nil {
		httpErr(w, http.StatusConflict, "no activation request has been generated on this appliance yet")
		return
	}
	var req activation.Request
	if json.Unmarshal(raw, &req) != nil {
		httpErr(w, http.StatusInternalServerError, "the stored activation request is unreadable")
		return
	}
	id, err := s.idStore.EnsureLocalKeypair()
	if err != nil || id == nil {
		httpErr(w, http.StatusInternalServerError, "identity unavailable")
		return
	}
	pubRaw, _ := base64.RawStdEncoding.DecodeString(id.PublicKeyB64)
	identityFpr := ""
	if len(pubRaw) == ed25519.PublicKeySize {
		identityFpr = activation.KeyID(ed25519.PublicKey(pubRaw))
	}
	vendorRaw, err := os.ReadFile(envOr("SCD_VENDOR_PUB", "/etc/stayconnect/vendor-license.pub"))
	if err != nil || len(vendorRaw) != ed25519.PublicKeySize {
		httpErr(w, http.StatusServiceUnavailable,
			"this appliance has no vendor trust key installed, so it cannot verify an activation package")
		return
	}
	alreadyAssigned := false
	if d := s.assignmentStore().Doc(); d != nil && d.TenantID != "" {
		alreadyAssigned = true
	}
	serial := firstNonEmpty(s.serial, id.Serial)
	if reason := activation.AcceptForFirstActivation(ed25519.PublicKey(vendorRaw), &pkg,
		serial, identityFpr, req.RequestID, req.Nonce,
		alreadyAssigned, time.Now()); reason != "" {
		httpErr(w, http.StatusForbidden, "package rejected: "+reason)
		return
	}
	// Verify the signed assignment before anything is written: the package signature proves the vendor sent it, not that the
	// assignment survives the check every boot applies (same function, registry and binding as the online path).
	if _, reason := verifyPackageAssignment(&pkg, id.PublicKeyB64, serial, s.assignmentStore(), time.Now()); reason != "" {
		httpErr(w, http.StatusForbidden, "package rejected: "+reason)
		return
	}
	// Bind the licence check to this identity and the appliance id the package names, since the identity is not loaded yet.
	if s.lic != nil {
		s.lic.SetLocalIdentity(lic.LocalIdentity{
			ApplianceID: pkg.ApplianceID, IdentityKeyFingerprint: applianceauthKeyIDFromB64(id.PublicKeyB64),
			Serial: s.hw.Serial, HardwareFingerprint: s.hw.Fingerprint, WANMAC: s.hw.WANMAC,
		})
	}
	if err := s.applyActivationPackage(r, &pkg); err != nil {
		if errors.Is(err, errPackageConsumed) {
			httpErr(w, http.StatusConflict, "this activation package has already been used")
			return
		}
		httpErr(w, http.StatusBadRequest, "activation failed, nothing was changed: "+err.Error())
		return
	}
	// Restart so the daemon comes up with the new identity, assignment and licence, as the online path does.
	go func() {
		time.Sleep(500 * time.Millisecond)
		restartAfterActivation()
	}()
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "activated", "package_id": pkg.PackageID,
		"tenant_id": pkg.TenantID, "site_id": pkg.SiteID,
		"note": "assignment, trust material and licence installed; the service is restarting",
	})
}

var errPackageConsumed = errors.New("package already consumed")

// restartAfterActivation is a variable so a test can apply a package without restarting.
var restartAfterActivation = func() { _ = exec.Command("systemctl", "restart", "stayconnect-scd").Run() }

// verifyPackageAssignment checks the signed assignment exactly as the online agent and every boot do: trusted signing key,
// bound to the package's appliance id and this identity key, a newer version, and a granting state. Returns "" when it verifies.
func verifyPackageAssignment(pkg *activation.Package, identityPubB64, serial string, store *assignment.Store,
	now time.Time) (*assignment.Document, string) {
	var doc assignment.Document
	if err := json.Unmarshal(pkg.Assignment, &doc); err != nil {
		return nil, "the signed assignment inside the package is unreadable"
	}
	if doc.TenantID != pkg.TenantID || doc.SiteID != pkg.SiteID {
		return nil, "the signed assignment disagrees with the package about customer or site"
	}
	if !assignment.Grants(doc.State) {
		return nil, "the signed assignment does not assign this appliance to a site"
	}
	reg, _ := currentRegistryOnDisk()
	have := int64(0)
	if rec, err := store.Load(); err == nil && rec != nil {
		have = rec.Version
	}
	if reason := assignment.AcceptForRegistry(reg, &doc, pkg.ApplianceID, serial,
		applianceauthKeyIDFromB64(identityPubB64), have, now); reason != "" {
		return nil, "the signed assignment inside it could not be verified (" + reason + ")"
	}
	return &doc, ""
}

// applianceauthKeyIDFromB64 is the hex identity-key fingerprint assignment and licence carry (not the base64url form the activation envelope uses; see internal/activation.KeyID).
func applianceauthKeyIDFromB64(pubB64 string) string {
	raw, err := base64.RawStdEncoding.DecodeString(pubB64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return ""
	}
	return applianceauth.KeyID(ed25519.PublicKey(raw))
}

// applyActivationPackage performs the durable writes and undoes them on any failure. Single-use is claimed first (a package
// reaching apply must not be reusable even if a later step fails); everything after is reversible. The licence goes in last
// because its anti-rollback may legitimately refuse, and the appliance id is adopted only once it is in.
func (s *server) applyActivationPackage(r *http.Request, pkg *activation.Package) error {
	// 0. Journal first, fsynced, before any durable change: a crash from here on is found at the next boot.
	jb, _ := json.Marshal(pkg)
	if err := writeFileSync(s.activationJournalPath(), jb, 0o600); err != nil {
		return errors.New("could not journal the activation: " + err.Error())
	}
	// The journal is removed only on the success path at the end, or by the rollback below.
	// 1. Single-use ledger, reboot-persistent.
	if s.db != nil {
		tag, err := s.db.Exec(r.Context(),
			`INSERT INTO edge_offline_packages (package_id, nonce) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
			pkg.PackageID, pkg.Nonce)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errPackageConsumed
		}
	}
	// 2. Signed assignment: Adopt itself verifies nothing. The assignment bytes are covered by the package's vendor signature
	// (activation.AcceptForFirstActivation above), and every boot re-verifies the stored document against the trust registry
	// (assignment.Resolve), so a package cannot smuggle in an unsigned tenant/site.
	var doc assignment.Document
	if err := json.Unmarshal(pkg.Assignment, &doc); err != nil {
		return errors.New("the signed assignment inside the package is unreadable")
	}
	if doc.TenantID != pkg.TenantID || doc.SiteID != pkg.SiteID {
		return errors.New("the signed assignment disagrees with the package about tenant or site")
	}
	if err := s.assignmentStore().Adopt(&doc); err != nil {
		return errors.New("signed assignment refused: " + err.Error())
	}
	undoAssignment := func() {
		if err := s.assignmentStore().Clear(); err != nil {
			slog.Error("could not roll back the assignment after a failed activation", "err", err)
		}
	}
	// 3. Trust material, so this appliance can later reach Central over mTLS.
	if pkg.CABundlePEM != "" {
		p := filepath.Join(envOr("SCD_CERT_DIR", "/etc/stayconnect/certs"), "ca-bundle.crt")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			undoAssignment()
			return err
		}
		if err := os.WriteFile(p, []byte(pkg.CABundlePEM), 0o644); err != nil {
			undoAssignment()
			return errors.New("could not install the trust bundle")
		}
	}
	// 4. Signed licence, last: it has its own anti-rollback and is the step most likely to refuse.
	if s.lic != nil && len(pkg.LicenseEnvelope) > 0 && string(pkg.LicenseEnvelope) != "null" {
		if _, err := s.lic.Install(r.Context(), pkg.LicenseEnvelope); err != nil {
			undoAssignment()
			if errors.Is(err, lic.ErrRollback) {
				return errors.New("the licence in this package is older than the one installed")
			}
			return errors.New("licence install failed: " + err.Error())
		}
	}
	// 5. Adopt the appliance id Central minted, last, so an appliance never claims an id for an incomplete activation.
	if err := s.idStore.AdoptApplianceID(pkg.ApplianceID); err != nil {
		undoAssignment()
		return errors.New("could not adopt the appliance identity: " + err.Error())
	}
	// 6. Clearing the journal makes the activation done; a crash before is recovered at next boot, after has nothing to recover.
	_ = os.Remove(s.activationJournalPath())
	return nil
}

// recoverInterruptedActivation runs at startup: a journal means a package was mid-apply, and the appliance must end up fully
// activated or unassigned as before. It decides by what actually landed, not by how far the code got.
func (s *server) recoverInterruptedActivation() {
	path := s.activationJournalPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		return // nothing was in flight
	}
	var pkg activation.Package
	if json.Unmarshal(raw, &pkg) != nil {
		// An unreadable journal cannot be completed, and leaving it would block every future activation.
		// Roll back to unassigned, which is the safe half of the two allowed outcomes.
		slog.Error("activation journal is unreadable; rolling back to unassigned")
		_ = s.assignmentStore().Clear()
		_ = os.Remove(path)
		return
	}
	doc := s.assignmentStore().Doc()
	assigned := doc != nil && doc.TenantID == pkg.TenantID && pkg.TenantID != ""
	identityAdopted := s.applID == pkg.ApplianceID && pkg.ApplianceID != ""
	licensed := s.lic != nil && string(s.lic.State()) != "unlicensed"

	switch {
	case assigned && identityAdopted && licensed:
		// Everything landed; only the journal removal was lost.
		slog.Info("interrupted offline activation was complete; clearing the journal",
			"package_id", pkg.PackageID)
		_ = os.Remove(path)
	default:
		// Partial: clear the assignment so the appliance is plainly unassigned, but leave the single-use ledger entry: the package was spent and re-using it is the replay the ledger prevents.
		slog.Warn("offline activation was interrupted and is incomplete; rolling back to unassigned",
			"package_id", pkg.PackageID, "assigned", assigned, "identity", identityAdopted,
			"licensed", licensed)
		if err := s.assignmentStore().Clear(); err != nil {
			slog.Error("could not clear the assignment during activation rollback", "err", err)
		}
		_ = os.Remove(path)
	}
}

// writeFileSync fsyncs both the file and its directory so the bytes survive power loss; a journal that is not durable is not a journal.
func writeFileSync(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// offlineNonceHex is a random 128-bit identifier for the request/nonce pair.
func offlineNonceHex() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

package main

// OFFLINE FIRST ACTIVATION — the appliance half.
//
// Two endpoints, one per file the operator carries:
//
//	GET  /v1/setup/activation-request   emit the request (generates the local keypair if absent)
//	POST /v1/setup/activation-package   verify and apply the package Central returned
//
// This is SEPARATE from /v1/setup/offline-import, which transports a licence to an appliance that is already
// assigned. That path is untouched; nothing here changes its envelope, its checks or its ledger.
//
// APPLYING IS ATOMIC IN THE ONLY sense that matters operationally: the appliance either ends up assigned AND
// licensed, or it ends up exactly as it started. The order below is chosen so that the step most likely to
// fail runs first and the irreversible one runs last, and any failure after a durable write is undone before
// returning. See applyActivationPackage.

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
	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/hwid"
	lic "github.com/stayconnect/enterprise/license"
)

// activationRequestFile keeps the emitted request beside the identity so the package that answers it can be
// matched. Without it a second request would invalidate the first, and an operator carrying a package back
// would be told it "does not answer this appliance's request" with no way to see why.
func (s *server) activationRequestPath() string {
	return filepath.Join(envOr("SCD_IDENTITY_DIR", "/etc/stayconnect/identity"), "activation-request.json")
}

// activationJournalPath is the crash-recovery marker.
//
// Applying a package touches four durable places -- a ledger row, the assignment file, the trust bundle and
// the licence store -- and no filesystem gives us one transaction across them. Power loss between any two
// leaves an appliance that is partly activated, which is the one outcome that must never survive a reboot:
// a box assigned to a hotel but unlicensed, or licensed but with no identity, is worse than one that is
// plainly unactivated, because it looks finished.
//
// So the package is journalled BEFORE anything is written and the journal is removed only after everything
// is. On boot, a journal that is still present means an activation was interrupted, and
// recoverInterruptedActivation either finishes it or undoes it. Both directions are safe because every step
// is idempotent: Adopt is version-checked, the licence store is anti-rollback, and AdoptApplianceID refuses
// to repoint an identity.
func (s *server) activationJournalPath() string {
	return filepath.Join(envOr("SCD_IDENTITY_DIR", "/etc/stayconnect/identity"), "activation-in-progress.json")
}

// setupActivationRequest emits the offline activation request.
//
// It generates the identity keypair locally if this appliance has none. The private half never leaves: the
// request carries the public key and a signature made with the private one, which is the whole proof.
func (s *server) setupActivationRequest(w http.ResponseWriter, r *http.Request) {
	if s.enrolled || s.applID != "" {
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
	// Reuse an outstanding request rather than minting a new one on every click: a fresh nonce would
	// silently invalidate the package the operator is already carrying back.
	if raw, err := os.ReadFile(s.activationRequestPath()); err == nil {
		var prev activation.Request
		if json.Unmarshal(raw, &prev) == nil && activation.VerifyRequest(&prev) &&
			prev.PublicKey == id.PublicKeyB64 {
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

// setupActivationPackage verifies and applies the package Central issued for this appliance's request.
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
	// The outstanding request this package must answer.
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
	if reason := activation.AcceptForFirstActivation(ed25519.PublicKey(vendorRaw), &pkg,
		firstNonEmpty(s.serial, id.Serial), identityFpr, req.RequestID, req.Nonce,
		alreadyAssigned, time.Now()); reason != "" {
		httpErr(w, http.StatusForbidden, "package rejected: "+reason)
		return
	}
	if err := s.applyActivationPackage(r, &pkg); err != nil {
		if errors.Is(err, errPackageConsumed) {
			httpErr(w, http.StatusConflict, "this activation package has already been used")
			return
		}
		httpErr(w, http.StatusBadRequest, "activation failed, nothing was changed: "+err.Error())
		return
	}
	// Restart so the daemon comes up holding the new identity, assignment and licence, exactly as the online
	// path does after enrollment.
	go func() {
		time.Sleep(500 * time.Millisecond)
		_ = exec.Command("systemctl", "restart", "stayconnect-scd").Run()
	}()
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "activated", "package_id": pkg.PackageID,
		"tenant_id": pkg.TenantID, "site_id": pkg.SiteID,
		"note": "assignment, trust material and licence installed; the service is restarting",
	})
}

var errPackageConsumed = errors.New("package already consumed")

// applyActivationPackage performs the durable writes, and undoes them on any failure.
//
// ORDER MATTERS. Single-use is claimed FIRST, because a package that reaches the point of being applied must
// not be usable a second time even if a later step fails. Everything after that is reversible, and is
// reversed on error: the assignment is cleared, the adopted appliance id is only written once the licence is
// in, and the licence is the last thing installed because it is the step with its own anti-rollback that may
// legitimately refuse.
func (s *server) applyActivationPackage(r *http.Request, pkg *activation.Package) error {
	// 0. JOURNAL FIRST, fsynced, before a single durable change. If the power fails from here on, the next
	//    boot finds this file and knows an activation was in flight.
	jb, _ := json.Marshal(pkg)
	if err := writeFileSync(s.activationJournalPath(), jb, 0o600); err != nil {
		return errors.New("could not journal the activation: " + err.Error())
	}
	// The journal is removed only on the success path at the end, or by the rollback below.
	// 1. Single-use ledger, reboot-persistent.
	if s.db != nil {
		// edge_offline_packages is declared by migration 0048; it is not created here.
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
	// 2. Signed assignment. Adopt verifies its OWN signature and version anti-rollback; this envelope only
	//    carried it. A package cannot smuggle in a tenant/site that the assignment authority did not sign.
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
	// 5. Adopt the appliance id Central minted. Last, so an appliance never claims an id for an activation
	//    that did not complete.
	if err := s.idStore.AdoptApplianceID(pkg.ApplianceID); err != nil {
		undoAssignment()
		return errors.New("could not adopt the appliance identity: " + err.Error())
	}
	// 6. Everything durable is in place. Clearing the journal is what makes the activation "done"; a crash
	//    before this point is recovered on the next boot, and a crash after it has nothing left to recover.
	_ = os.Remove(s.activationJournalPath())
	return nil
}

// recoverInterruptedActivation runs at startup. A journal means a package was being applied when the
// appliance stopped, and the appliance must end up in exactly one of two states: fully activated, or
// unassigned as it was before.
//
// It decides by looking at what actually landed rather than at how far the code got, because a crash does
// not leave a note about which line it reached.
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
		// Anything else is partial. Undo it: clear the assignment so the appliance is plainly unassigned,
		// and leave the single-use ledger entry standing -- the package was spent, and re-using it would be
		// exactly the replay the ledger exists to prevent. The operator generates a fresh one.
		slog.Warn("offline activation was interrupted and is incomplete; rolling back to unassigned",
			"package_id", pkg.PackageID, "assigned", assigned, "identity", identityAdopted,
			"licensed", licensed)
		if err := s.assignmentStore().Clear(); err != nil {
			slog.Error("could not clear the assignment during activation rollback", "err", err)
		}
		_ = os.Remove(path)
	}
}

// writeFileSync writes and fsyncs both the file and its directory, so the bytes survive a power loss rather
// than sitting in a cache that never reaches the disk. A journal that is not durable is not a journal.
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

// firstNonEmpty returns the first value that is not empty.
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

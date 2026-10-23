package assignment

// The ONE place an appliance decides which tenant and site it belongs to: every daemon resolves it here, against the persisted Central-signed assignment
// and the trust registry anchored by the manufacture-time root. There is deliberately no second way to obtain a scope (no env var, no locally signed
// document, no per-daemon key): a second authority could scope a daemon to any tenant, and a security check implemented per daemon drifts.

import (
	"crypto/ed25519"
	"encoding/base64"
	"log/slog"
	"os"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/applianceauth"
)

// The on-disk locations of the assignment chain; deployment facts each daemon reads from its own environment.
type Paths struct {
	Dir          string
	RegistryPath string
	// RegistryRootPath is the manufacture-time root public key that anchors the signed registry.
	RegistryRootPath string
	// TrustPath is the legacy plain trust file, consulted ONLY where no signed registry has ever been
	// persisted (pre-rollout). It is never a fallback for a signed registry that fails to verify.
	TrustPath string
}

type ApplianceBinding struct {
	ApplianceID  string
	Serial       string
	PublicKeyB64 string
}

// Outcome says WHY a resolution came out as it did, because "no tenant/site" has two causes: OutcomeAbsent (nothing adopted: factory-clean or unenrolled)
// and OutcomeUnverifiable (a document IS present and this appliance cannot stand behind it). Collapsing them treats a REJECTED appliance as a NEW one
// and hides signature, registry or binding failures.
type Outcome int

const (
	// OutcomeAbsent: no assignment document; the correct state of an unassigned appliance, which does no tenant work.
	OutcomeAbsent Outcome = iota
	// OutcomeUnverifiable: a document exists but failed verification (no trusted registry, unreadable file, signer outside the registry, another
	// appliance, unacceptable version). Always an error to the caller.
	OutcomeUnverifiable
	// OutcomeNotGranting: the document VERIFIED but its state confers no scope (unassigned, revoked, decommissioned): the appliance is no longer entitled
	// to operate. Also an error to the caller.
	OutcomeNotGranting
	OutcomeGranted
)

func (o Outcome) String() string {
	switch o {
	case OutcomeAbsent:
		return "absent"
	case OutcomeUnverifiable:
		return "unverifiable"
	case OutcomeNotGranting:
		return "not_granting"
	case OutcomeGranted:
		return "granted"
	}
	return "unknown"
}

// TenantID/SiteID are non-empty ONLY for a verified, granting assignment bound to this appliance; every caller must treat empty as "do no tenant work".
// State and Version stay empty/zero for anything that did not verify (as scd has always reported); read Outcome when the reason matters.
type Resolution struct {
	TenantID string
	SiteID   string
	State    string
	Version  int64
	// Read Outcome, not the emptiness of TenantID, to tell "never assigned" from "assignment refused".
	Outcome Outcome
	// Stale reports a verified document past its expires_at. It is NOT a de-authorisation: a stale assignment
	// keeps a hotel running through a Central outage. Callers log it; they do not act on it.
	Stale bool
}

func (r Resolution) Assigned() bool { return r.Outcome == OutcomeGranted }

// Refused reports a document that exists but yields no scope; a caller must treat it as an error, not as unassigned.
func (r Resolution) Refused() bool {
	return r.Outcome == OutcomeUnverifiable || r.Outcome == OutcomeNotGranting
}

// Returns the assignment-key registry to verify against and its source (signed, legacy-plain or none). Prefers the verified signed registry
// (its last-known-good copy survives a Central outage) and REFUSES to fall back to the unsigned file once a signed registry exists: downgrading would
// let anyone who can write that file authorise a rogue key.
func TrustedRegistry(p Paths, log *slog.Logger) (*Registry, string) {
	if rootPub, err := LoadRootPub(p.RegistryRootPath); err == nil {
		rs := &RegistryStore{Path: p.RegistryPath, RootPub: rootPub}
		if reg := rs.Trusted(); reg != nil {
			return reg, "signed"
		}
		if rs.FileExists() {
			if log != nil {
				log.Error("assignment: on-disk signed registry failed verification — refusing to downgrade to the unsigned trust file")
			}
			return nil, "none"
		}
	}
	// Pre-rollout only: no signed registry has ever been persisted on this appliance.
	reg, err := LoadRegistry(p.TrustPath)
	if err != nil {
		return nil, "none"
	}
	return reg, "legacy-plain"
}

// Loads the persisted assignment and RE-VERIFIES it: the signature must come from a registry key permitted to verify and the document must be bound
// to THIS appliance. FAIL CLOSED is the default and only fallback: absent, unreadable, unverifiable, unbound or non-granting all return an unassigned Resolution.
func Resolve(p Paths, ident ApplianceBinding, now time.Time, log *slog.Logger) Resolution {
	store := &Store{Dir: p.Dir}
	rec, err := store.Load()
	if err != nil {
		// A MISSING file is a factory-clean appliance; an UNREADABLE one is a document this appliance cannot stand behind, and calling it "not assigned yet"
		// would hide a corrupt assignment.
		if os.IsNotExist(err) {
			return Resolution{Outcome: OutcomeAbsent}
		}
		if log != nil {
			log.Error("assignment: persisted assignment is present but unreadable — refusing to operate on it",
				"err", err.Error())
		}
		return Resolution{Outcome: OutcomeUnverifiable}
	}
	if rec == nil || rec.Current == nil {
		return Resolution{Outcome: OutcomeAbsent}
	}
	d := rec.Current

	reg, src := TrustedRegistry(p, log)
	if reg == nil {
		// A document exists and there is no trust anchor to judge it by. That is unverifiable, NOT absent.
		if log != nil {
			log.Error("assignment: no trusted registry — refusing to trust the persisted assignment")
		}
		return Resolution{Outcome: OutcomeUnverifiable}
	}
	if src != "signed" && log != nil {
		log.Warn("assignment: verifying against legacy plain trust file (no signed registry yet)")
	}

	fpr := ""
	if raw, e := base64.RawStdEncoding.DecodeString(ident.PublicKeyB64); e == nil && len(raw) == ed25519.PublicKeySize {
		fpr = applianceauth.KeyID(ed25519.PublicKey(raw))
	}
	// haveVersion = d.Version-1 so the persisted document itself is admissible; a verify_only signer still verifies, letting an appliance reboot mid-rotation.
	if reason := AcceptForRegistry(reg, d, ident.ApplianceID, ident.Serial, fpr, d.Version-1, now); reason != "" {
		if log != nil {
			log.Error("assignment: persisted assignment FAILED verification — ignoring it",
				"reason", reason, "signer_key_id", d.SignerKeyID, "version", d.Version)
		}
		return Resolution{Outcome: OutcomeUnverifiable}
	}

	out := Resolution{State: d.State, Version: d.Version, Stale: IsExpired(d, now)}
	if out.Stale && log != nil {
		log.Warn("assignment: STALE (past expires_at, not refreshed) — retaining last-known-good tenant/site",
			"version", d.Version, "expired_at", d.ExpiresAt)
	}
	if Grants(d.State) {
		out.TenantID, out.SiteID = d.TenantID, d.SiteID
		out.Outcome = OutcomeGranted
		return out
	}
	// Verified, and the appliance holds no scope. State/Version are reported so a caller can say WHICH terminal state.
	out.Outcome = OutcomeNotGranting
	return out
}

package assignment

// THE ONE PLACE AN APPLIANCE DECIDES WHICH TENANT AND SITE IT BELONGS TO.
//
// Every daemon that needs tenant/site authority resolves it here, against the persisted Central-signed
// assignment and the trust registry anchored by the manufacture-time registry root. There is deliberately no
// second way to obtain a scope: not an env var, not a locally signed document, not a per-daemon key.
//
// This used to live only in cmd/scd. When pmsd needed the same answer, the shortest path was to give it its
// own signed-document format and its own appliance-local Ed25519 keypair — which produced a SECOND authority
// that could assert a tenant and site on its own, with a key that never left the box and that nothing in the
// Central chain had blessed. Anyone able to write that key and that file could scope the PMS connector to any
// tenant they liked, and the canonical assignment would never have been consulted. Promoting the real check
// here removes that possibility rather than documenting it.
//
// Duplicating the logic per daemon would have been almost as bad: a security check implemented twice is a
// security check that drifts, and the copy that drifts is the one nobody is reading during an incident.

import (
	"crypto/ed25519"
	"encoding/base64"
	"log/slog"
	"os"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/applianceauth"
)

// Paths carries the on-disk locations of the assignment chain. Each daemon reads them from its own
// environment (they are deployment facts, not per-service policy), so the resolver takes them rather than
// reaching for globals.
type Paths struct {
	// Dir holds assignment.json — the persisted Central-signed document.
	Dir string
	// RegistryPath is the signed trust registry of assignment-signing keys.
	RegistryPath string
	// RegistryRootPath is the manufacture-time root public key that anchors the signed registry.
	RegistryRootPath string
	// TrustPath is the legacy plain trust file, consulted ONLY where no signed registry has ever been
	// persisted (pre-rollout). It is never a fallback for a signed registry that fails to verify.
	TrustPath string
}

// ApplianceBinding is the identity the assignment must be bound to: this appliance and no other.
type ApplianceBinding struct {
	ApplianceID  string
	Serial       string
	PublicKeyB64 string
}

// Outcome says WHY a resolution came out the way it did. It exists because "no tenant/site" has two very
// different causes and callers must be able to act on the difference:
//
//	OutcomeAbsent       nothing has been adopted — a factory-clean or not-yet-enrolled appliance
//	OutcomeUnverifiable a document IS present and this appliance cannot stand behind it
//
// Collapsing those two is how a REJECTED appliance gets treated as a NEW one. Both produce empty tenant/site,
// so a caller inspecting only the fields sees the same thing, silently reads "not enrolled yet", and reports
// a signature, registry or binding failure as though the box had simply never been assigned. The failure that
// most needs to be loud becomes the one that looks most routine.
type Outcome int

const (
	// OutcomeAbsent — no assignment document exists. Not an error: it is the correct state of an appliance
	// that has not been assigned, and the correct response is to do no tenant work.
	OutcomeAbsent Outcome = iota
	// OutcomeUnverifiable — a document exists but did not survive verification: no trusted registry, an
	// unreadable or corrupt file, a signature from a key outside the registry, a binding to a different
	// appliance, or a version the appliance will not accept. Always an error to the caller.
	OutcomeUnverifiable
	// OutcomeNotGranting — the document VERIFIED, and its state confers no scope (unassigned, revoked,
	// decommissioned). Distinct from Unverifiable because the document is trustworthy; what it says is that
	// this appliance is no longer entitled to operate. Also always an error to the caller.
	OutcomeNotGranting
	// OutcomeGranted — verified, bound to this appliance, and granting. The only outcome carrying scope.
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

// Resolution is the outcome. TenantID/SiteID are non-empty ONLY when a verified, granting assignment bound to
// this appliance exists. Everything else — absent, unverifiable, revoked, bound elsewhere — yields empty
// tenant/site, which every caller must treat as "do no tenant work".
//
// State and Version deliberately stay EMPTY/zero for anything that did not verify. That is what scd has always
// reported for an unverifiable document, and callers that key on them keep their existing behaviour; Outcome
// is the field to read when the reason matters.
type Resolution struct {
	TenantID string
	SiteID   string
	State    string
	Version  int64
	// Outcome classifies the result. Read this, not the emptiness of TenantID, to tell "never assigned" from
	// "assignment refused".
	Outcome Outcome
	// Stale reports a verified document past its expires_at. It is NOT a de-authorisation: a stale assignment
	// keeps a hotel running through a Central outage. Callers log it; they do not act on it.
	Stale bool
}

// Assigned reports whether a usable tenant/site scope was resolved.
func (r Resolution) Assigned() bool { return r.Outcome == OutcomeGranted }

// Refused reports a document that exists but yields no scope — unverifiable or non-granting. It is the
// condition a caller must treat as an error rather than as an unassigned appliance.
func (r Resolution) Refused() bool {
	return r.Outcome == OutcomeUnverifiable || r.Outcome == OutcomeNotGranting
}

// TrustedRegistry returns the assignment-key registry to verify against, and where it came from
// ("signed" | "legacy-plain" | "none"). It prefers the verified signed registry, whose last-known-good copy
// survives a Central outage, and REFUSES to fall back to the unsigned trust file once a signed registry
// exists on disk — downgrading there would let anyone who can write that file authorise a rogue signing key.
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

// Resolve loads the persisted assignment and RE-VERIFIES it against the trust registry: the signature must
// come from a registry key permitted to verify, and the document must be bound to THIS appliance. A document
// signed by a retired key — or by the license / command / update / CA key, none of which are in the registry
// — is refused, and the appliance is left unassigned rather than operating on an identity it cannot verify.
//
// FAIL CLOSED IS THE DEFAULT AND THE ONLY FALLBACK. Absent, unreadable, unverifiable, unbound or non-granting
// all return an unassigned Resolution. A factory-clean appliance therefore resolves to no tenant and no site,
// which is exactly what it should do: nothing.
func Resolve(p Paths, ident ApplianceBinding, now time.Time, log *slog.Logger) Resolution {
	store := &Store{Dir: p.Dir}
	rec, err := store.Load()
	if err != nil {
		// A MISSING file and an UNREADABLE one are different answers. Load reports both as an error, so the
		// distinction is made here: no file at all is a factory-clean appliance; a file that exists and
		// cannot be parsed is a document this appliance cannot stand behind, and saying "not assigned yet"
		// about a corrupt assignment would hide exactly the case worth investigating.
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
		return Resolution{Outcome: OutcomeAbsent} // nothing adopted yet
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
	// haveVersion = d.Version-1 so the persisted document itself is admissible. A verify_only signer still
	// verifies here — that is what lets an appliance holding an older-key assignment reboot cleanly
	// mid-rotation.
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
	// Verified, and it says this appliance holds no scope. The document is trustworthy; what it grants is
	// nothing. State/Version are reported so a caller can say WHICH terminal state, which is why scd has
	// always surfaced them for this case.
	out.Outcome = OutcomeNotGranting
	return out
}

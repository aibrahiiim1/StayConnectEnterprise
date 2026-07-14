package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var (
	ErrBadSignature = errors.New("license signature invalid")
	ErrUnknownKey   = errors.New("license signed by unknown vendor key")
	ErrMalformed    = errors.New("license payload malformed")
)

// Verifier holds the trusted vendor public key(s). Appliances embed/install
// only public keys. Multiple keys support rotation.
type Verifier struct {
	keys map[string]ed25519.PublicKey // key_id -> pub
}

func NewVerifier(pubs ...ed25519.PublicKey) *Verifier {
	v := &Verifier{keys: map[string]ed25519.PublicKey{}}
	for _, p := range pubs {
		v.keys[KeyIDFor(p)] = p
	}
	return v
}

// AddKey registers an additional trusted vendor public key (rotation).
func (v *Verifier) AddKey(pub ed25519.PublicKey) { v.keys[KeyIDFor(pub)] = pub }

// Verify checks the envelope signature against the trusted keys and returns
// the embedded Document. It does NOT evaluate time-based state — that is
// Evaluate's job — but it does run structural validation.
func (v *Verifier) Verify(e *Envelope) (*Document, error) {
	pub, ok := v.keys[e.KeyID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownKey, e.KeyID)
	}
	payload, err := base64.StdEncoding.DecodeString(e.PayloadB64)
	if err != nil {
		return nil, fmt.Errorf("%w: payload b64: %v", ErrMalformed, err)
	}
	sig, err := base64.StdEncoding.DecodeString(e.SigB64)
	if err != nil {
		return nil, fmt.Errorf("%w: sig b64: %v", ErrMalformed, err)
	}
	if !ed25519.Verify(pub, payload, sig) {
		return nil, ErrBadSignature
	}
	var d Document
	if err := json.Unmarshal(payload, &d); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if err := d.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return &d, nil
}

// Evaluation is the locally computed operational judgment for a document.
type Evaluation struct {
	State State `json:"state"`
	// Doc is the evaluated document (nil only when no license is installed).
	Doc *Document `json:"doc,omitempty"`
	// InGraceUntil / restricted boundaries surfaced for the admin UI.
	GraceUntil      time.Time `json:"grace_until"`
	RestrictedUntil time.Time `json:"restricted_until"`
	// ClockRollback is set when the system clock is behind the
	// monotonically persisted high-water mark by more than the tolerance;
	// evaluation then used the persisted time instead of the wall clock.
	ClockRollback bool `json:"clock_rollback,omitempty"`
	// LastCloudValidation is informational (sync health warning), it does
	// not degrade guest function while the document itself is valid.
	LastCloudValidation time.Time `json:"last_cloud_validation,omitempty"`
	CloudStale          bool      `json:"cloud_stale,omitempty"`
}

// Evaluate computes the operational state of a verified document.
//
// State timeline (document time only — cloud reachability never takes a
// valid hotel offline):
//
//	issued_at ─────────── valid_until ── +grace ── +2×grace ──▶
//	        Active            │ GracePeriod │ Restricted │ Expired
//
// Suspended (doc status) and Revoked (revocation notice) override the
// timeline. CloudStale is a warning flag, not a state change: it trips when
// the last successful cloud validation is older than offline_grace_days.
func Evaluate(d *Document, now, lastCloudValidation time.Time, revoked bool) Evaluation {
	ev := Evaluation{Doc: d, LastCloudValidation: lastCloudValidation}
	grace := time.Duration(d.EffectiveGraceDays()) * 24 * time.Hour
	ev.GraceUntil = d.ValidUntil.Add(grace)
	// Simple model (v3): the timeline is Active → Grace → Expired; there is no
	// Restricted window. Legacy documents keep the historical 2×grace window.
	if d.SchemaVersion >= 3 {
		ev.RestrictedUntil = ev.GraceUntil
	} else {
		ev.RestrictedUntil = d.ValidUntil.Add(2 * grace)
	}

	// Cloud staleness stays keyed to the OFFLINE grace allowance.
	offline := time.Duration(d.OfflineGraceDays) * 24 * time.Hour
	if !lastCloudValidation.IsZero() && offline > 0 && now.Sub(lastCloudValidation) > offline {
		ev.CloudStale = true
	}

	switch {
	case revoked:
		ev.State = StateRevoked
	case d.Status == DocSuspended:
		ev.State = StateSuspended
	case !d.ValidFrom.IsZero() && now.Before(d.ValidFrom):
		// Not yet valid: no new authorization until ValidFrom.
		ev.State = StateExpired
	case !now.After(d.ValidUntil):
		ev.State = StateActive
	case !now.After(ev.GraceUntil):
		ev.State = StateGracePeriod
	case !now.After(ev.RestrictedUntil):
		ev.State = StateRestricted
	default:
		ev.State = StateExpired
	}
	return ev
}

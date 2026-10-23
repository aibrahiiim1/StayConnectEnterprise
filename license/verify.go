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

// Appliances hold public keys only; multiple keys permit rotation.
type Verifier struct {
	keys map[string]ed25519.PublicKey
}

func NewVerifier(pubs ...ed25519.PublicKey) *Verifier {
	v := &Verifier{keys: map[string]ed25519.PublicKey{}}
	for _, p := range pubs {
		v.keys[KeyIDFor(p)] = p
	}
	return v
}

func (v *Verifier) AddKey(pub ed25519.PublicKey) { v.keys[KeyIDFor(pub)] = pub }

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

type Evaluation struct {
	State           State     `json:"state"`
	Doc             *Document `json:"doc,omitempty"`
	GraceUntil      time.Time `json:"grace_until"`
	RestrictedUntil time.Time `json:"restricted_until"`
	// Evaluation uses persisted time when the clock moves back beyond tolerance.
	ClockRollback bool `json:"clock_rollback,omitempty"`
	// Cloud staleness warns but does not interrupt a valid license.
	LastCloudValidation time.Time `json:"last_cloud_validation,omitempty"`
	CloudStale          bool      `json:"cloud_stale,omitempty"`
}

// Suspension and revocation override the timeline; cloud staleness only warns.
func Evaluate(d *Document, now, lastCloudValidation time.Time, revoked bool) Evaluation {
	ev := Evaluation{Doc: d, LastCloudValidation: lastCloudValidation}
	grace := time.Duration(d.EffectiveGraceDays()) * 24 * time.Hour
	ev.GraceUntil = d.ValidUntil.Add(grace)
	// Older documents retain an extra restricted grace window.
	if d.SchemaVersion >= 3 {
		ev.RestrictedUntil = ev.GraceUntil
	} else {
		ev.RestrictedUntil = d.ValidUntil.Add(2 * grace)
	}

	// Cloud silence uses the offline allowance, not renewal grace.
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

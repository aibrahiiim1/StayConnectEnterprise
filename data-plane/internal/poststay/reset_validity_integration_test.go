//go:build integration && phase5

package poststay

import (
	"context"
	"testing"
	"time"
)

// AUDIT H-17. A reset rotates the PIN and leaves the validity window alone unless the operator asks for it to
// be restarted. It used to move valid_until to now+24h on every reset while the console said only the PIN
// changes.
func TestIntegration_ResetKeepsValidityUnlessAsked(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := store(t, p)
	got := issue(t, s, f)
	ctx := context.Background()
	op := "33333333-3333-3333-3333-333333333333"

	var before time.Time
	if err := p.QueryRow(ctx, `SELECT valid_until FROM iam_v2.post_stay_profiles WHERE id=$1`, got.Profile).Scan(&before); err != nil {
		t.Fatal(err)
	}
	kept, err := s.Reset(ctx, ResetRequest{Tenant: f.tenant, Site: f.site, Profile: got.Profile,
		Operator: op, Reason: "guest lost the printout"})
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if !kept.Expires.Equal(before) {
		t.Fatalf("a reset without extend moved the validity from %v to %v", before, kept.Expires)
	}
	ext, err := s.Reset(ctx, ResetRequest{Tenant: f.tenant, Site: f.site, Profile: got.Profile,
		Operator: op, Reason: "extend for a late flight", ValidFor: 48 * time.Hour})
	if err != nil {
		t.Fatalf("reset with extend: %v", err)
	}
	if d := time.Until(ext.Expires); d < 47*time.Hour || d > 49*time.Hour {
		t.Fatalf("an extending reset set validity %v from now, want ~48h", d)
	}
}

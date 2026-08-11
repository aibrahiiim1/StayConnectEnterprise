package main

// netd derives its OWN Phase-3 mode. It could have taken the tenant, site and appliance out of the submitted
// plan and trusted them — every field is right there in the envelope — but then the envelope would be both the
// claim and the thing that authorizes the claim. Reading the flags, the enrollment identity and the signed
// assignment from the same sources every other daemon uses means a submitted plan can only ever be CHECKED
// against this appliance's real scope, never define it.

import (
	"context"
	"log/slog"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/identity"
)

// phase3Mode is netd's own answer to "is Phase 3 live here, and for whom".
type phase3Mode struct {
	Active      bool
	TenantID    string
	SiteID      string
	ApplianceID string
	AssignGen   int64
}

func loadPhase3Mode(ctx context.Context, getenv func(string) string) (phase3Mode, error) {
	// Every lookup goes through the SAME getenv the flags came from. Mixing os.Getenv in here would make the
	// resolved scope depend on process state a caller cannot see or control.
	dirOr := func(k, d string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return d
	}
	cfg, err := iamv2.LoadPMSConfigFromEnv(getenv)
	if err != nil {
		return phase3Mode{}, err
	}
	if !cfg.CheckoutGraceOn() {
		// DARK: no scope is resolved at all, so there is nothing for a plan to match and every submission is
		// refused. Note that netd does not even read the assignment while dark.
		return phase3Mode{}, nil
	}

	idStore := &identity.Store{Dir: dirOr("NETD_IDENTITY_DIR", "/etc/stayconnect/identity")}
	ident, err := idStore.LoadOrEnroll(ctx, "", "", "", false)
	if err != nil || ident == nil || ident.ApplianceID == "" {
		// Live enforcement on an appliance that cannot state its own identity would have to accept a plan on
		// the plan's word. Staying inactive is the honest failure: nothing is enforced and health says so.
		slog.Error("netd: phase3 is enabled but the appliance identity is unavailable — shaping stays inactive", "err", err)
		return phase3Mode{}, nil
	}
	asg := &assignment.Store{Dir: dirOr("NETD_ASSIGNMENT_DIR", "/etc/stayconnect/assignment")}
	tenant, site, _, gen := asg.Resolved()
	if tenant == "" || site == "" {
		slog.Error("netd: phase3 is enabled but no signed assignment resolves a tenant/site — shaping stays inactive")
		return phase3Mode{}, nil
	}
	return phase3Mode{
		Active:      true,
		TenantID:    tenant,
		SiteID:      site,
		ApplianceID: ident.ApplianceID,
		AssignGen:   gen,
	}, nil
}

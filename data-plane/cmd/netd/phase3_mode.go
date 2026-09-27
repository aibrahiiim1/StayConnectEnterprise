package main

// netd derives its OWN Phase-3 mode. It could have taken the tenant, site and appliance out of the submitted
// plan and trusted them — every field is right there in the envelope — but then the envelope would be both the
// claim and the thing that authorizes the claim. Reading the flags, the enrollment identity and the signed
// assignment from the same sources every other daemon uses means a submitted plan can only ever be CHECKED
// against this appliance's real scope, never define it.

import (
	"context"
	"log/slog"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
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
	if !cfg.EnforcementOn() {
		// DARK: no scope is resolved at all, so there is nothing for a plan to match and every submission is
		// refused. Note that netd does not even read the assignment while dark.
		//
		// The gate is the ENFORCEMENT PLANE's, not Checkout Grace's. It used to be CheckoutGraceOn, which made
		// the network writer depend on an unrelated post-departure feature: an appliance running Room
		// authentication had scd minting sessions while netd refused every plan, so two real guests were
		// granted access that nothing ever put in the kernel.
		return phase3Mode{}, nil
	}

	// THE VERIFIED SCOPE, from the same resolver scd uses: signature, trust registry and binding to THIS
	// appliance. This used to read assignment.json raw, so a document scd refused as unverifiable was still the
	// scope the enforcement plane accepted plans for. netd needs only the PUBLIC identity to verify.
	_ = ctx
	identityDir := dirOr("NETD_IDENTITY_DIR", "/etc/stayconnect/identity")
	r, applianceID := assignment.ResolveLocal(assignment.PathsFromEnv(getenv, "NETD"), identityDir, time.Now(), slog.Default())
	if applianceID == "" {
		// Live enforcement on an appliance that cannot state its own identity would have to accept a plan on
		// the plan's word. Staying inactive is the honest failure: nothing is enforced and health says so.
		slog.Error("netd: phase3 is enabled but the appliance identity is unavailable — shaping stays inactive",
			"outcome", r.Outcome.String())
		return phase3Mode{}, nil
	}
	if !r.Assigned() {
		slog.Error("netd: phase3 is enabled but no VERIFIED signed assignment resolves a tenant/site — shaping stays inactive",
			"outcome", r.Outcome.String())
		return phase3Mode{}, nil
	}
	return phase3Mode{
		Active:      true,
		TenantID:    r.TenantID,
		SiteID:      r.SiteID,
		ApplianceID: applianceID,
		AssignGen:   r.Version,
	}, nil
}

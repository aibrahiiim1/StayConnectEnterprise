package main

// netd derives its own scope from flags, enrollment identity and the signed assignment, never from the
// submitted plan, so a plan is only checked against the real scope and cannot define it.

import (
	"context"
	"log/slog"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

type phase3Mode struct {
	Active      bool
	TenantID    string
	SiteID      string
	ApplianceID string
	AssignGen   int64
}

func loadPhase3Mode(ctx context.Context, getenv func(string) string) (phase3Mode, error) {
	// Use the same getenv the flags came from, not os.Getenv.
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
		// Dark: no scope resolves, so every submission is refused. The gate is the enforcement plane's, not
		// Checkout Grace's.
		return phase3Mode{}, nil
	}

	// Verified scope from the same resolver scd uses (signature, trust registry, binding to this appliance).
	_ = ctx
	identityDir := dirOr("NETD_IDENTITY_DIR", "/etc/stayconnect/identity")
	r, applianceID := assignment.ResolveLocal(assignment.PathsFromEnv(getenv, "NETD"), identityDir, time.Now(), slog.Default())
	if applianceID == "" {
		// Without its own identity netd would have to trust the plan's word, so it stays inactive.
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

package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/deployment"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/modules"
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
	lic "github.com/stayconnect/enterprise/license"
)

// initModules builds the deployment ceiling and the module resolver. scd hosts the resolver because it holds
// the licence; edged and portald ask scd (GET /v1/modules) rather than re-deriving anything.
//
// An incoherent deployment flag set is a startup failure, exactly as each underlying configuration already is.
func (s *server) initModules() error {
	c, err := deployment.Load(os.Getenv)
	if err != nil {
		return err
	}
	s.ceiling = c
	var local modules.Local
	if s.db != nil {
		local = modules.PgLocal{DB: s.db}
	}
	var licSrc modules.Licence
	if s.lic != nil {
		licSrc = s.lic
	}
	s.modules = modules.New(c, licSrc, local, s.identitySwitches)
	slog.Info(c.Summary())
	return nil
}

// identitySwitches maps the Sign-in methods switches onto the identity modules: that screen stays the single
// local source for SMS, email and social sign-in.
func (s *server) identitySwitches(ctx context.Context, tenantID string) (map[string]bool, error) {
	var cfg *tenantcfg.AuthMethods
	var err error
	if s.methodSwitches != nil {
		cfg, err = s.methodSwitches(ctx)
	} else if s.db != nil {
		cfg, err = tenantcfg.Load(ctx, s.db, tenantID)
	} else {
		return nil, errors.New("no site database")
	}
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	if cfg.SMS != nil && cfg.SMS.Enabled {
		out[lic.ModuleSMSOTP] = true
	}
	if cfg.Email != nil && cfg.Email.Enabled {
		out[lic.ModuleEmailOTP] = true
	}
	for _, p := range cfg.Social {
		if p != nil && p.Enabled {
			out[lic.ModuleSocialLogin] = true
		}
	}
	return out, nil
}

// moduleSnapshot resolves every module for this appliance's current site. With no resolver (a test server)
// nothing optional is effective: fail closed.
func (s *server) moduleSnapshot(ctx context.Context) modules.Snapshot {
	if s.modules == nil {
		return modules.Snapshot{Modules: map[string]modules.State{}}
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return s.modules.Resolve(cctx, s.tenID, s.siteID)
}

// moduleEffective reports whether module id may execute now.
func (s *server) moduleEffective(ctx context.Context, id string) bool {
	return s.moduleSnapshot(ctx).Effective(id)
}

// modulesStatus: GET /v1/modules. Local facts only; never contacts Central.
func (s *server) modulesStatus(w http.ResponseWriter, r *http.Request) {
	snap := s.moduleSnapshot(r.Context())
	var siteType *string
	if _, doc := s.resolveOwnAssignment(time.Now().UTC()); doc != nil && doc.SiteType != "" {
		st := doc.SiteType
		siteType = &st
	}
	licState := string(lic.StateUnlicensed)
	if s.lic != nil {
		licState = string(s.lic.State())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"site_type":     siteType,
		"license_state": licState,
		"modules":       snap.Modules,
		"deployment": map[string]bool{
			string(deployment.CapClientPackages):     s.ceiling.Available(deployment.CapClientPackages),
			string(deployment.CapPackageAdmin):       s.ceiling.Available(deployment.CapPackageAdmin),
			string(deployment.CapCardLive):           s.ceiling.Available(deployment.CapCardLive),
			string(deployment.CapRoomChargeReview):   s.ceiling.Available(deployment.CapRoomChargeReview),
			string(deployment.CapRoomChargeTransmit): s.ceiling.Available(deployment.CapRoomChargeTransmit),
		},
	})
}

// siteAcquisitionMethods is the commerce engine's method gate: which optional acquisition modules are
// effective here right now (licensed, enabled, deployed and ready). Free and Voucher are core.
func (s *server) siteAcquisitionMethods(ctx context.Context) iamv2.SiteMethods {
	snap := s.moduleSnapshot(ctx)
	return iamv2.SiteMethods{
		PaidAccess: snap.Effective(lic.ModulePaidAccess),
		Card:       snap.Effective(lic.ModuleCardPayment),
		RoomCharge: snap.Effective(lic.ModuleRoomCharge),
	}
}

// registerModuleProbes gives the resolver the readiness and history answers only their owners can give.
func (s *server) registerModuleProbes() {
	if s.modules == nil {
		return
	}
	s.modules.SetProbe(lic.ModuleCardPayment, s.cardReadiness)
	s.modules.SetRecordsProbe(lic.ModuleCardPayment, s.cardHasRecords)
}

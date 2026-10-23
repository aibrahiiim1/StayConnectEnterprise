// Package modules is OneGate's single module resolver. Each optional module is evaluated against four gates that
// are never collapsed: Deployed (deployment ceiling), Licensed (signed licence in Active/Grace), Enabled (the site's
// switch; modules without one are enabled when licensed) and Ready (runtime readiness).
//
// Execution requires all four. Management requires only that the module is deployed and either licensed or already
// holds records; readiness never hides a management surface. Site Type is not an input. Every failure mode is
// closed: a local-state read error means not enabled, an unknown module is not deployed, and a missing probe means
// not ready.
package modules

import (
	"context"
	"sort"

	"github.com/stayconnect/enterprise/data-plane/internal/deployment"
	lic "github.com/stayconnect/enterprise/license"
)

const (
	ReasonNotDeployed     = "NOT_DEPLOYED"
	ReasonNotLicensed     = "NOT_LICENSED"
	ReasonLicenceInactive = "LICENCE_NOT_ACTIVE"
	ReasonDependency      = "DEPENDENCY_NOT_EFFECTIVE"
	ReasonDisabledBySite  = "DISABLED_BY_SITE"
	ReasonLocalUnknown    = "LOCAL_STATE_UNAVAILABLE"
	ReasonNotReady        = "NOT_READY"
)

type Licence interface {
	ModuleEnabled(id string) bool       // authorised AND the licence state allows features
	AuthorizedModules() map[string]bool // authorised, regardless of state (display)
}

// Local reads the site's switches for the switchable modules (site_module_get).
type Local interface {
	SiteModules(ctx context.Context, tenantID, siteID string) (map[string]bool, error)
}

// IdentitySwitches reads the Sign-in methods switches that are the local enablement of the identity modules.
type IdentitySwitches func(ctx context.Context, tenantID string) (map[string]bool, error)

// Probe reports runtime readiness for one module. detail is a short stable code (e.g. PROVIDER_UNREACHABLE).
type Probe func(ctx context.Context, tenantID, siteID string) (ready bool, details []string)

// RecordsProbe reports whether the site holds records of a module (history that must stay manageable).
type RecordsProbe func(ctx context.Context, tenantID, siteID string) bool

// switchable are the modules whose local switch is iam_v2.site_module_settings.
var switchable = map[string]bool{
	lic.ModuleHospitality: true, lic.ModulePaidAccess: true, lic.ModuleCardPayment: true, lic.ModuleRoomCharge: true,
}

// identity are the modules whose local switch is the Sign-in methods screen.
var identity = map[string]bool{
	lic.ModuleSMSOTP: true, lic.ModuleWhatsAppOTP: true, lic.ModuleEmailOTP: true, lic.ModuleSocialLogin: true,
}

// Resolver evaluates module state. Probes may be registered after construction by the subsystem that owns
// the readiness question (payment, posting).
type Resolver struct {
	Ceiling  deployment.Ceiling
	Licence  Licence
	Local    Local
	Identity IdentitySwitches
	probes   map[string]Probe
	records  map[string]RecordsProbe
}

func New(c deployment.Ceiling, l Licence, local Local, id IdentitySwitches) *Resolver {
	return &Resolver{Ceiling: c, Licence: l, Local: local, Identity: id, probes: map[string]Probe{}, records: map[string]RecordsProbe{}}
}

func (r *Resolver) SetProbe(id string, p Probe) { r.probes[id] = p }

func (r *Resolver) SetRecordsProbe(id string, p RecordsProbe) { r.records[id] = p }

type State struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	Requires   []string `json:"requires"`
	Deployed   bool     `json:"deployed"`
	Authorized bool     `json:"authorized"` // named by the licence, regardless of licence state
	Licensed   bool     `json:"licensed"`   // authorised and the licence state allows it
	Switchable bool     `json:"switchable"` // has a local switch on the Modules screen
	Enabled    bool     `json:"enabled"`
	Ready      bool     `json:"ready"`
	Effective  bool     `json:"effective"`  // may execute now
	Manageable bool     `json:"manageable"` // configuration, history and recovery surfaces are available
	Reasons    []string `json:"reasons"`
	Readiness  []string `json:"readiness,omitempty"`
}

type Snapshot struct {
	Modules map[string]State `json:"modules"`
}

func (s Snapshot) Effective(id string) bool { return s.Modules[id].Effective }

func (s Snapshot) Manageable(id string) bool { return s.Modules[id].Manageable }

func (r *Resolver) Resolve(ctx context.Context, tenantID, siteID string) Snapshot {
	var local map[string]bool
	localErr := error(nil)
	if r.Local != nil && tenantID != "" && siteID != "" {
		local, localErr = r.Local.SiteModules(ctx, tenantID, siteID)
	} else {
		localErr = errNoLocal
	}
	var ids map[string]bool
	idErr := error(nil)
	if r.Identity != nil && tenantID != "" {
		ids, idErr = r.Identity(ctx, tenantID)
	} else {
		idErr = errNoLocal
	}
	var authorized map[string]bool
	if r.Licence != nil {
		authorized = r.Licence.AuthorizedModules()
	}

	out := Snapshot{Modules: map[string]State{}}
	for _, spec := range lic.Registry() {
		st := State{ID: spec.ID, Label: spec.Label, Requires: append([]string{}, spec.Requires...), Reasons: []string{}}
		st.Deployed = r.Ceiling.ModuleDeployed(spec.ID)
		if !st.Deployed {
			st.Reasons = append(st.Reasons, ReasonNotDeployed)
		}
		st.Authorized = authorized[spec.ID]
		st.Licensed = r.Licence != nil && r.Licence.ModuleEnabled(spec.ID)
		switch {
		case !st.Authorized && !st.Licensed:
			st.Reasons = append(st.Reasons, ReasonNotLicensed)
		case st.Authorized && !st.Licensed:
			st.Reasons = append(st.Reasons, ReasonLicenceInactive)
		}
		switch {
		case switchable[spec.ID]:
			st.Switchable = true
			if localErr != nil {
				st.Reasons = append(st.Reasons, ReasonLocalUnknown)
			} else {
				st.Enabled = local[spec.ID]
			}
		case identity[spec.ID]:
			if idErr != nil {
				st.Reasons = append(st.Reasons, ReasonLocalUnknown)
			} else {
				st.Enabled = ids[spec.ID]
			}
		default:
			st.Enabled = st.Licensed
		}
		if !st.Enabled && (switchable[spec.ID] || identity[spec.ID]) && localErr == nil && idErr == nil {
			st.Reasons = append(st.Reasons, ReasonDisabledBySite)
		}
		// Readiness is probed once the module is deployed and licensed, before the site enables it, so the administrator
		// sees what is missing; an unprobeable module is not ready.
		st.Ready = true
		if p, ok := r.probes[spec.ID]; ok {
			if st.Deployed && st.Licensed {
				ready, details := p(ctx, tenantID, siteID)
				st.Ready = ready
				st.Readiness = details
				if !ready && st.Enabled {
					st.Reasons = append(st.Reasons, ReasonNotReady)
				}
			} else {
				st.Ready = false
			}
		}
		st.Effective = st.Deployed && st.Licensed && st.Enabled && st.Ready
		hasRecords := false
		if rp, ok := r.records[spec.ID]; ok && st.Deployed && !st.Licensed {
			hasRecords = rp(ctx, tenantID, siteID)
		}
		st.Manageable = st.Deployed && (st.Licensed || hasRecords)
		out.Modules[spec.ID] = st
	}
	// A module is effective only while every dependency is effective (e.g. card_payment needs paid_access).
	for changed := true; changed; {
		changed = false
		for id, st := range out.Modules {
			if !st.Effective {
				continue
			}
			for _, dep := range st.Requires {
				if !out.Modules[dep].Effective {
					st.Effective = false
					st.Reasons = append(st.Reasons, ReasonDependency+":"+dep)
					out.Modules[id] = st
					changed = true
					break
				}
			}
		}
	}
	for id, st := range out.Modules {
		sort.Strings(st.Reasons)
		out.Modules[id] = st
	}
	return out
}

type resolverError string

func (e resolverError) Error() string { return string(e) }

const errNoLocal = resolverError("local state source not configured")

package main

// MODULES IN THE ADMIN CONSOLE.
//
// scd holds the licence and runs the four-gate module resolver (deployment ceiling, licence, local switch,
// readiness). edged asks it (GET /v1/modules) and does two things with the answer:
//
//  1. Every module-owned operator surface is gated per request on MANAGEABILITY: the module is deployed and
//     is licensed or already holds records. Readiness is deliberately NOT part of this gate: an unreachable
//     provider or PMS must stop new client execution (scd enforces that) while configuration, diagnostics,
//     history, reconciliation and recovery stay available to restore service.
//  2. /capabilities stops reporting a surface whose module is not manageable, so the navigation (which
//     already hides surfaces that are not reported) follows the licence and the site's choice without a second
//     model in the browser. The full module state is reported alongside.
//
// FAIL CLOSED: if the module state cannot be read, module-owned surfaces answer 503 and are not reported.
// Core surfaces are never module-gated.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	lic "github.com/stayconnect/enterprise/license"
)

// surfaceModules maps a module-owned surface to the module(s) that own it. A surface is manageable when ANY
// of its modules is manageable (the financial screens serve both card payment and room charge).
var surfaceModules = map[string][]string{
	"pms-stays":                 {lic.ModuleHospitality},
	"pms-events":                {lic.ModuleHospitality},
	"pms-resolutions":           {lic.ModuleHospitality},
	"guest-signin-attempts":     {lic.ModuleHospitality},
	"guest-signin-credentials":  {lic.ModuleHospitality},
	"guest-signin-protection":   {lic.ModuleHospitality},
	"guest-signin-restrictions": {lic.ModuleHospitality},
	"checkout-grace":            {lic.ModuleHospitality},
	"operational-alerts":        {lic.ModuleHospitality},
	"pms-interfaces":            {lic.ModuleHospitality},
	"pms-routing":               {lic.ModuleHospitality},
	"pms-source-conflicts":      {lic.ModuleHospitality},
	"pms-reconciliation":        {lic.ModuleHospitality},
	"pms-roster-reconciliation": {lic.ModuleHospitality},
	"post-stay-profiles":        {lic.ModuleHospitality},
	"stay-transfers":            {lic.ModuleHospitality},
	"pms-financial-onboarding":  {lic.ModuleRoomCharge},
	"payment-providers":         {lic.ModuleCardPayment},
	"financial-review":          {lic.ModuleRoomCharge, lic.ModuleCardPayment},
	"financial-ops":             {lic.ModuleRoomCharge, lic.ModuleCardPayment},
}

// historySurfaces are the module-owned surfaces that hold HISTORY, AUDIT, RECONCILIATION or RECOVERY. They
// stay reachable while the module is manageable -- licensed, OR no longer licensed but still holding records --
// because withdrawing a licence must never make evidence or recovery inaccessible.
//
// Every OTHER module-owned surface is day-to-day configuration or operation, and is served only while the
// module is LICENSED (and deployed). A site without Hospitality therefore does not see PMS connection, Room
// sign-in protection, grace or routing screens merely because it once ran a PMS; it keeps its stays, PMS
// activity, sign-in history, reconciliation and financial review.
var historySurfaces = map[string]bool{
	"pms-stays":                 true,
	"pms-events":                true,
	"pms-resolutions":           true,
	"guest-signin-attempts":     true,
	"guest-signin-credentials":  true,
	"pms-reconciliation":        true,
	"pms-roster-reconciliation": true,
	"operational-alerts":        true,
	"financial-review":          true,
	"financial-ops":             true,
}

// surfaceServed is the one rule for a module-owned surface: history needs the module manageable, everything
// else needs it licensed and deployed.
func surfaceServed(name string, st moduleState) bool {
	if historySurfaces[name] {
		return st.Manageable
	}
	return st.Licensed && st.Deployed
}

// moduleState is one module as scd reports it (internal/modules.State).
type moduleState struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	Requires   []string `json:"requires"`
	Deployed   bool     `json:"deployed"`
	Authorized bool     `json:"authorized"`
	Licensed   bool     `json:"licensed"`
	Switchable bool     `json:"switchable"`
	Enabled    bool     `json:"enabled"`
	Ready      bool     `json:"ready"`
	Effective  bool     `json:"effective"`
	Manageable bool     `json:"manageable"`
	Reasons    []string `json:"reasons"`
	Readiness  []string `json:"readiness,omitempty"`
}

type moduleReport struct {
	SiteType     *string                `json:"site_type"`
	LicenseState string                 `json:"license_state"`
	Modules      map[string]moduleState `json:"modules"`
	Deployment   map[string]bool        `json:"deployment"`
}

// moduleCache keeps scd's answer for a few seconds so a page load that asks several surfaces does not ask scd
// several times. A switch change invalidates it.
type moduleCache struct {
	mu  sync.Mutex
	at  time.Time
	rep *moduleReport
}

const moduleCacheTTL = 3 * time.Second

// moduleReport returns scd's module state, or ok=false when it cannot be read (callers fail closed).
func (s *server) moduleReport(ctx context.Context) (*moduleReport, bool) {
	if s.modCache == nil || s.scd == nil {
		return nil, false
	}
	s.modCache.mu.Lock()
	defer s.modCache.mu.Unlock()
	if s.modCache.rep != nil && time.Since(s.modCache.at) < moduleCacheTTL {
		return s.modCache.rep, true
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	st, raw, err := s.scd.call(cctx, http.MethodGet, "/v1/modules", nil)
	if err != nil || st != http.StatusOK {
		return nil, false
	}
	var rep moduleReport
	if err := json.Unmarshal(raw, &rep); err != nil || rep.Modules == nil {
		return nil, false
	}
	s.modCache.rep, s.modCache.at = &rep, time.Now()
	return &rep, true
}

func (s *server) invalidateModules() {
	if s.modCache == nil {
		return
	}
	s.modCache.mu.Lock()
	s.modCache.rep = nil
	s.modCache.mu.Unlock()
}

// surfaceManageable reports whether a surface may be served. Core surfaces always may. ok=false means the
// module state could not be read.
func (s *server) surfaceManageable(ctx context.Context, name string) (manageable bool, ok bool) {
	mods, owned := surfaceModules[name]
	if !owned {
		return true, true
	}
	if s.modCache == nil {
		// A server assembled without module wiring (focused unit tests) has no gate to apply. Production
		// always wires it in main, where the cache is created unconditionally.
		return true, true
	}
	rep, ok := s.moduleReport(ctx)
	if !ok {
		return false, false
	}
	for _, id := range mods {
		if surfaceServed(name, rep.Modules[id]) {
			return true, true
		}
	}
	return false, true
}

// moduleGate is applied by mountResource to every surface. It is a no-op for core surfaces.
func (s *server) moduleGate(name string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if _, owned := surfaceModules[name]; !owned {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			manageable, ok := s.surfaceManageable(r.Context(), name)
			if !ok {
				jsonErr(w, http.StatusServiceUnavailable, "module_state_unavailable",
					"The appliance could not determine which modules are available. Try again shortly.")
				return
			}
			if !manageable {
				jsonErr(w, http.StatusNotFound, "module_not_available",
					"This feature is not licensed or not enabled at this site.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// filterSurfaces removes module-owned surfaces that are not manageable. When the module state cannot be read,
// every module-owned surface is removed (fail closed) and core surfaces are kept.
func (s *server) filterSurfaces(ctx context.Context, all []string) ([]string, *moduleReport) {
	var rep *moduleReport
	if s.modCache != nil {
		rep, _ = s.moduleReport(ctx)
	}
	out := make([]string, 0, len(all))
	for _, n := range all {
		mods, owned := surfaceModules[n]
		if !owned || s.modCache == nil {
			out = append(out, n)
			continue
		}
		if rep == nil {
			continue
		}
		for _, id := range mods {
			if surfaceServed(n, rep.Modules[id]) {
				out = append(out, n)
				break
			}
		}
	}
	return out, rep
}

// ---- the Modules screen -----------------------------------------------------------------------------------

func (s *server) modulesRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.getModules)
	r.Get("/changes", s.getModuleChanges)
	r.With(s.requireRole("modules", permWrite)).Put("/{id}", s.putModule)
	return r
}

func (s *server) getModules(w http.ResponseWriter, r *http.Request) {
	rep, ok := s.moduleReport(r.Context())
	if !ok {
		jsonErr(w, http.StatusServiceUnavailable, "module_state_unavailable", "module state could not be read")
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *server) getModuleChanges(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
		SELECT module_id, changed_at, changed_by, COALESCE(change_reason,''), old_enabled, new_enabled
		  FROM iam_v2.site_module_changes
		 WHERE tenant_id = $1::uuid AND site_id = $2::uuid
		 ORDER BY changed_at DESC LIMIT 100`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "changes_unreadable", err.Error())
		return
	}
	defer rows.Close()
	type change struct {
		Module    string    `json:"module"`
		ChangedAt time.Time `json:"changed_at"`
		ChangedBy string    `json:"changed_by"`
		Reason    string    `json:"reason"`
		Old       *bool     `json:"old_enabled"`
		New       bool      `json:"new_enabled"`
	}
	out := []change{}
	for rows.Next() {
		var c change
		if err := rows.Scan(&c.Module, &c.ChangedAt, &c.ChangedBy, &c.Reason, &c.Old, &c.New); err != nil {
			jsonErr(w, http.StatusInternalServerError, "changes_unreadable", err.Error())
			return
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": out})
}

// putModule switches a licensed module on or off for this site. Password step-up and a reason: turning a
// module off stops new client execution immediately (history and recovery stay available), so it is a
// deliberate act by a named operator.
func (s *server) putModule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	switch id {
	case lic.ModuleHospitality, lic.ModulePaidAccess, lic.ModuleCardPayment, lic.ModuleRoomCharge:
	default:
		jsonErr(w, http.StatusBadRequest, "not_switchable", "this module has no local switch")
		return
	}
	var in struct {
		Enabled  *bool  `json:"enabled"`
		Reason   string `json:"reason"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil || in.Enabled == nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "enabled is required")
		return
	}
	if len(strings.TrimSpace(in.Reason)) > 500 {
		jsonErr(w, http.StatusBadRequest, "bad_request", "reason is limited to 500 characters")
		return
	}
	if !s.reauth(r, in.Password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
		return
	}
	rep, ok := s.moduleReport(r.Context())
	if !ok {
		jsonErr(w, http.StatusServiceUnavailable, "module_state_unavailable", "module state could not be read")
		return
	}
	st := rep.Modules[id]
	// Enabling needs the licence and every dependency's licence. Disabling is always allowed.
	if *in.Enabled {
		if !st.Licensed {
			jsonErr(w, http.StatusConflict, "not_licensed", "This module is not licensed for this site.")
			return
		}
		for _, dep := range st.Requires {
			if !rep.Modules[dep].Enabled {
				jsonErr(w, http.StatusConflict, "dependency_disabled", "Enable "+rep.Modules[dep].Label+" first.")
				return
			}
		}
	} else {
		for oid, other := range rep.Modules {
			for _, dep := range other.Requires {
				if dep == id && other.Enabled {
					jsonErr(w, http.StatusConflict, "dependant_enabled", "Disable "+rep.Modules[oid].Label+" first.")
					return
				}
			}
		}
	}
	sess := sessFrom(r.Context())
	actor := protectionActor(sess)
	if actor == "" {
		jsonErr(w, http.StatusForbidden, "forbidden", "the change could not be attributed to an operator")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var version int64
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.site_module_set($1::uuid,$2::uuid,$3,$4,$5,NULLIF($6,''))`,
		s.tenantID, s.siteID, id, *in.Enabled, actor, strings.TrimSpace(in.Reason)).Scan(&version); err != nil {
		jsonErr(w, http.StatusInternalServerError, "module_not_saved", err.Error())
		return
	}
	s.invalidateModules()
	s.audit(r, "module.update", "module", id, map[string]any{
		"previous_enabled": st.Enabled, "enabled": *in.Enabled, "config_version": version,
		"reason": strings.TrimSpace(in.Reason),
	})
	rep2, ok := s.moduleReport(r.Context())
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"saved": true})
		return
	}
	writeJSON(w, http.StatusOK, rep2)
}

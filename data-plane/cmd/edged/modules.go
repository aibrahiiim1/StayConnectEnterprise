package main

// scd resolves module state; edged gates each module-owned surface per request on manageability (deployed, and
// licensed or still holding records) and stops reporting unmanageable surfaces in /capabilities.
// Readiness is deliberately not part of the gate: an unreachable provider or PMS must stop new client execution
// (scd enforces that) while configuration, history and recovery stay available.
// Fails closed: if module state cannot be read, module-owned surfaces answer 503. Core surfaces are never gated.

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

// surfaceModules maps a module-owned surface to its owning modules; any one being manageable is enough.
var surfaceModules = map[string][]string{
	"pms-stays":                 {lic.ModuleHospitality},
	"pms-events":                {lic.ModuleHospitality},
	"pms-event-decisions":       {lic.ModuleHospitality},
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
	// Provider screens for optional sign-in methods: refused by the API too, so a direct call cannot configure an unlicensed method.
	"social-providers":       {lic.ModuleSocialLogin},
	"notification-providers": {lic.ModuleEmailOTP, lic.ModuleSMSOTP, lic.ModuleWhatsAppOTP},
}

// historySurfaces hold history, audit, reconciliation or recovery and stay reachable while the module is manageable
// (even if no longer licensed), so withdrawing a licence never makes evidence inaccessible. Every other
// module-owned surface is served only while the module is licensed and deployed.
var historySurfaces = map[string]bool{
	"pms-stays":                 true,
	"pms-events":                true,
	"pms-event-decisions":       true,
	"pms-resolutions":           true,
	"guest-signin-attempts":     true,
	"guest-signin-credentials":  true,
	"pms-reconciliation":        true,
	"pms-roster-reconciliation": true,
	"operational-alerts":        true,
	"financial-review":          true,
	"financial-ops":             true,
}

func surfaceServed(name string, st moduleState) bool {
	if historySurfaces[name] {
		return st.Manageable
	}
	return st.Licensed && st.Deployed
}

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

// moduleCache holds scd's answer briefly; a switch change invalidates it.
type moduleCache struct {
	mu  sync.Mutex
	at  time.Time
	rep *moduleReport
}

const moduleCacheTTL = 3 * time.Second

// moduleReport returns ok=false when the state cannot be read; callers fail closed.
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

// roomSignInInScope: room sign-in exists only where the Hotel module is licensed, deployed and switched on. A site without it
// is not asked to route room sign-in at all. Unreadable module state (or no module cache) keeps the room sign-in facts shown.
func (s *server) roomSignInInScope(ctx context.Context) bool {
	if s.modCache == nil {
		return true
	}
	rep, ok := s.moduleReport(ctx)
	if !ok {
		return true
	}
	h := rep.Modules["hospitality"]
	return h.Licensed && h.Deployed && h.Enabled
}

func (s *server) invalidateModules() {
	if s.modCache == nil {
		return
	}
	s.modCache.mu.Lock()
	s.modCache.rep = nil
	s.modCache.mu.Unlock()
}

// surfaceManageable: core surfaces always may be served; ok=false means the module state could not be read.
func (s *server) surfaceManageable(ctx context.Context, name string) (manageable bool, ok bool) {
	mods, owned := surfaceModules[name]
	if !owned {
		return true, true
	}
	if s.modCache == nil {
		// Only unit tests build a server without module wiring; main always creates the cache.
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

// filterSurfaces drops module-owned surfaces that are not manageable; if state cannot be read, all of them (fail closed).
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

// putModule needs password step-up: turning a module off stops new client execution immediately.
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
		s.reauthRefused(w, r, "password confirmation required")
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

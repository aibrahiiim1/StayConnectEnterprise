package main

// What a client network offers at sign-in, as a narrowing of what the site offers: methods can be switched off for the network, one can lead, and devices can be
// remembered for fewer days. Nothing here can switch on a method the licence, the deployment or the site does not already allow; scd applies it at every sign-in
// entry point (cmd/scd/signin_network.go).

import (
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

var signInKeys = map[string]bool{
	"voucher": true, "email_account": true, "guest_account": true, "sms": true, "whatsapp": true, "social": true, "pms": true, "open": true,
}

type networkSigninDoc struct {
	DisabledMethods []string `json:"disabled_methods"`
	PrimaryMethod   string   `json:"primary_method"`
	RememberDays    *int     `json:"remember_days"`
}

func (s *server) getNetworkSignin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.networkOfSite(w, r, id) {
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	out := networkSigninDoc{DisabledMethods: []string{}}
	var primary *string
	err := s.db.QueryRow(ctx, `SELECT disabled_methods, primary_method, remember_days FROM iam_v2.network_signin_overrides WHERE network_id=$1::uuid AND tenant_id=$2::uuid`,
		id, s.tenantID).Scan(&out.DisabledMethods, &primary, &out.RememberDays)
	if err != nil && err != pgx.ErrNoRows {
		jsonErr(w, http.StatusInternalServerError, "internal", "the network's sign-in settings could not be read")
		return
	}
	if primary != nil {
		out.PrimaryMethod = *primary
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) putNetworkSignin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.networkOfSite(w, r, id) {
		return
	}
	var in networkSigninDoc
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	seen := map[string]bool{}
	off := []string{}
	for _, m := range in.DisabledMethods {
		if !signInKeys[m] {
			jsonErr(w, http.StatusBadRequest, "validation", "unknown sign-in method: "+m)
			return
		}
		if !seen[m] {
			seen[m] = true
			off = append(off, m)
		}
	}
	sort.Strings(off)
	if in.PrimaryMethod != "" && (!signInKeys[in.PrimaryMethod] || seen[in.PrimaryMethod]) {
		jsonErr(w, http.StatusBadRequest, "validation", "the method that leads must be a known method that is not switched off on this network")
		return
	}
	if in.RememberDays != nil && (*in.RememberDays < 0 || *in.RememberDays > 365) {
		jsonErr(w, http.StatusBadRequest, "validation", "remember devices for 0 to 365 days")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	// No override at all is the absence of the row, so "inherit everything" and "never configured" are the same state.
	if len(off) == 0 && in.PrimaryMethod == "" && in.RememberDays == nil {
		if _, err := s.db.Exec(ctx, `DELETE FROM iam_v2.network_signin_overrides WHERE network_id=$1::uuid`, id); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "the network's sign-in settings could not be saved")
			return
		}
	} else if _, err := s.db.Exec(ctx, `
		INSERT INTO iam_v2.network_signin_overrides (network_id, tenant_id, site_id, disabled_methods, primary_method, remember_days)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, NULLIF($5,''), $6)
		ON CONFLICT (network_id) DO UPDATE SET disabled_methods = EXCLUDED.disabled_methods, primary_method = EXCLUDED.primary_method,
		       remember_days = EXCLUDED.remember_days, updated_at = now()`, id, s.tenantID, s.siteID, off, in.PrimaryMethod, in.RememberDays); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the network's sign-in settings could not be saved")
		return
	}
	s.audit(r, "network.signin_changed", "guest_network", id, map[string]any{"disabled": off, "primary": in.PrimaryMethod, "remember_days": in.RememberDays})
	writeJSON(w, http.StatusOK, networkSigninDoc{DisabledMethods: off, PrimaryMethod: in.PrimaryMethod, RememberDays: in.RememberDays})
}

// networkOfSite answers 404 for a network that is not this site's.
func (s *server) networkOfSite(w http.ResponseWriter, r *http.Request, id string) bool {
	if s.tenantID == "" || !uuidPattern.MatchString(id) {
		jsonErr(w, http.StatusNotFound, "not_found", "no such client network")
		return false
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var one int
	if err := s.db.QueryRow(ctx, `SELECT 1 FROM guest_networks WHERE id=$1::uuid AND tenant_id=$2::uuid`, id, s.tenantID).Scan(&one); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "no such client network")
		return false
	}
	return true
}

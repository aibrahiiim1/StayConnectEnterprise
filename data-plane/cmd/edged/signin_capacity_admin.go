package main

// Sign-in limits and capacity settings (migration 0162; audit decisions AD-30/31/34/51/58): one-time-code caps and
// lifetime, the appliance-wide sign-in ceiling, the room sign-in pause exemption, device retention and the uploaded-image
// limits. Read through iam_v2.signin_capacity_settings_get and changed only through ..._set, which validates the bounds,
// names the operator and writes the append-only change log. Same permission key as the other sign-in settings
// ("auth-methods"), password step-up, and a mandatory reason.

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

type signinCapacityBound struct {
	Min     int    `json:"min"`
	Max     int    `json:"max"`
	Default any    `json:"default"`
	Unit    string `json:"unit"`
	Help    string `json:"help"`
}

// signinCapacityFields is the single list of settings: name, bounds, unit, explanation. The database CHECKs are the
// authority on the bounds; these mirror them so the console can validate before it submits.
var signinCapacityFields = []struct {
	Name string
	signinCapacityBound
}{
	{"otp_code_lifetime_minutes", signinCapacityBound{2, 30, 10, "minutes", "How long a one-time code works."}},
	{"otp_max_attempts", signinCapacityBound{3, 10, 5, "guesses", "Wrong guesses allowed on one code."}},
	{"otp_device_hourly_cap", signinCapacityBound{1, 20, 5, "codes per hour", "Codes one device may request for one number or address."}},
	{"otp_destination_hourly_cap", signinCapacityBound{1, 100, 15, "codes per hour", "Codes one number or address may be sent in all, from every device. Not below the per-device cap."}},
	{"otp_source_hourly_cap", signinCapacityBound{5, 500, 20, "codes per hour", "Codes one network source address may request, for all numbers together."}},
	{"signin_ceiling_per_minute", signinCapacityBound{600, 100000, nil, "attempts per minute", "Sign-in attempts the whole appliance accepts a minute. Empty follows the licence (one attempt per licensed guest every two minutes, never below 600)."}},
	{"network_pause_exempt_hours", signinCapacityBound{0, 168, 24, "hours", "A device that signed in with a room this recently is not stopped by a network-wide room sign-in pause. 0 turns the exemption off."}},
	{"device_retention_days", signinCapacityBound{7, 3650, 90, "days", "Devices not seen for this long, and not part of any session or package, are removed."}},
	{"portal_image_max_edge_px", signinCapacityBound{640, 4096, 2560, "pixels", "Uploaded portal background and logo images are scaled down so their longest side is at most this."}},
	{"portal_image_quality", signinCapacityBound{50, 95, 82, "percent", "Compression quality for uploaded portal images."}},
	// post_stay_signin_days (0179) is RETIRED by migration 0193: how long a guest can come back is the duration of the one
	// After Check-out Access policy (/after-checkout-access), not a second setting. The column stays as history.
	{"post_stay_device_attempts_per_minute", signinCapacityBound{3, 60, 10, "attempts per minute", "Post-stay sign-in: attempts one device may make a minute, right or wrong. More than this locks the device out."}},
	{"post_stay_ip_attempts_per_minute", signinCapacityBound{5, 200, 20, "attempts per minute", "Post-stay sign-in: attempts one network address may make a minute, whatever devices it uses. Guests behind one shared address count together."}},
	{"post_stay_wrong_answers_before_lockout", signinCapacityBound{3, 20, 5, "attempts per minute", "Post-stay sign-in: answers a device may give a minute for one stay (or, from a device with no stay of its own, in all) before it is locked out."}},
	{"post_stay_lockout_minutes", signinCapacityBound{1, 240, 15, "minutes", "Post-stay sign-in: how long a lockout lasts. It ends by itself; nobody has to release it."}},
	{"post_stay_new_device_room_attempts_per_minute", signinCapacityBound{3, 60, 10, "attempts per minute", "Post-stay sign-in: attempts a minute, from devices that were never on the stay, that name one room or one reservation number, whatever device identity they use. Stops guessing from changing devices; devices already on the stay are not counted."}},
	{"post_stay_appliance_attempts_per_minute", signinCapacityBound{20, 5000, 200, "attempts per minute", "Post-stay sign-in: attempts the whole appliance accepts a minute. Attempts that were already refused do not use it up."}},
}

func (s *server) signinCapacityRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.getSigninCapacity)
	r.Put("/", s.setSigninCapacity)
	return r
}

func (s *server) readSigninCapacity(r *http.Request) (map[string]any, int64, *time.Time, *string, error) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var raw []byte
	var version int64
	var at *time.Time
	var by *string
	err := s.db.QueryRow(ctx, `SELECT to_jsonb(g) - 'config_version' - 'updated_at' - 'updated_by', g.config_version, g.updated_at, g.updated_by
	     FROM iam_v2.signin_capacity_settings_get($1::uuid,$2::uuid) g`, s.tenantID, s.siteID).Scan(&raw, &version, &at, &by)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	var vals map[string]any
	if err := json.Unmarshal(raw, &vals); err != nil {
		return nil, 0, nil, nil, err
	}
	return vals, version, at, by, nil
}

func (s *server) getSigninCapacity(w http.ResponseWriter, r *http.Request) {
	if s.tenantID == "" || s.siteID == "" {
		writeAwaitingAssignment(w, "sign-in limits")
		return
	}
	vals, version, at, by, err := s.readSigninCapacity(r)
	if err != nil {
		if isUndefinedObject(err) {
			jsonErr(w, http.StatusServiceUnavailable, "not_available", "this appliance's database does not have the sign-in limits yet")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "the sign-in limits could not be read")
		return
	}
	delete(vals, "post_stay_signin_days") // retired (0193): the window is the After Check-out Access duration
	bounds := map[string]any{}
	for _, f := range signinCapacityFields {
		bounds[f.Name] = f.signinCapacityBound
	}
	type change struct {
		ChangedAt time.Time       `json:"changed_at"`
		ChangedBy string          `json:"changed_by"`
		Reason    string          `json:"reason"`
		Old       json.RawMessage `json:"old"`
		New       json.RawMessage `json:"new"`
	}
	changes := []change{}
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT changed_at, changed_by, change_reason, COALESCE(old_values,'null'::jsonb)::text, new_values::text
	     FROM iam_v2.signin_capacity_settings_changes WHERE tenant_id=$1 AND site_id=$2 ORDER BY changed_at DESC LIMIT 20`,
		s.tenantID, s.siteID)
	if err == nil {
		for rows.Next() {
			var c change
			var o, n string
			if rows.Scan(&c.ChangedAt, &c.ChangedBy, &c.Reason, &o, &n) == nil {
				c.Old, c.New = json.RawMessage(o), json.RawMessage(n)
				changes = append(changes, c)
			}
		}
		rows.Close()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": vals, "config_version": version, "updated_at": at, "updated_by": by,
		"bounds": bounds, "changes": changes,
	})
}

func (s *server) setSigninCapacity(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Settings map[string]*int `json:"settings"`
		Reason   string          `json:"reason"`
		Password string          `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	if len(in.Settings) == 0 {
		jsonErr(w, http.StatusBadRequest, "invalid_setting", "no setting was named")
		return
	}
	known := map[string]signinCapacityBound{}
	for _, f := range signinCapacityFields {
		known[f.Name] = f.signinCapacityBound
	}
	patch := map[string]any{}
	for name, v := range in.Settings {
		b, ok := known[name]
		if !ok {
			jsonErr(w, http.StatusBadRequest, "invalid_setting", "unknown setting "+name)
			return
		}
		if v == nil {
			// Only the ceiling may be cleared ("follow the licence"); everything else has a default, not a blank.
			if name != "signin_ceiling_per_minute" {
				jsonErr(w, http.StatusBadRequest, "invalid_setting", name+" cannot be empty")
				return
			}
			patch[name] = nil
			continue
		}
		if *v < b.Min || *v > b.Max {
			jsonErr(w, http.StatusBadRequest, "invalid_setting", name+" is outside its allowed range")
			return
		}
		patch[name] = *v
	}
	reason := strings.TrimSpace(in.Reason)
	if !validReason(reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a bounded reason (4-500 characters) is required")
		return
	}
	if s.tenantID == "" || s.siteID == "" {
		jsonErr(w, http.StatusConflict, "awaiting_assignment", "this appliance has no site yet, so nothing can be saved")
		return
	}
	actor, ok := s.stepUp(w, r, in.Password)
	if !ok {
		return
	}
	old, _, _, _, _ := s.readSigninCapacity(r)
	patchJSON, _ := json.Marshal(patch)
	ctx, cancel := dbCtx(r)
	defer cancel()
	var version int64
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.signin_capacity_settings_set($1::uuid,$2::uuid,$3::jsonb,$4,$5)`,
		s.tenantID, s.siteID, string(patchJSON), actor, reason).Scan(&version); err != nil {
		if isUndefinedObject(err) {
			jsonErr(w, http.StatusServiceUnavailable, "not_available", "this appliance's database does not have the sign-in limits yet")
			return
		}
		if isSettingRefusal(err) {
			jsonErr(w, http.StatusBadRequest, "invalid_setting", "that combination is not allowed (the destination ceiling cannot be below the per-device cap)")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "update_failed", "the sign-in limits could not be saved")
		return
	}
	s.audit(r, "signin_capacity.changed", "signin_capacity", s.siteID, map[string]any{
		"old": old, "new": patch, "config_version": version, "reason": reason})
	vals, _, _, _, _ := s.readSigninCapacity(r)
	writeJSON(w, http.StatusOK, map[string]any{"settings": vals, "config_version": version})
}

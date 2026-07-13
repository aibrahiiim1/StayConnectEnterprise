package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

type rebindReq struct {
	OldMAC           string `json:"old_mac"`
	NewMAC           string `json:"new_mac"`
	Reason           string `json:"reason"`
	Confirm          bool   `json:"confirm"`
	ValidDays        int    `json:"valid_days"`
	OfflineGraceDays int    `json:"offline_grace_days"`
}

func normalizeMAC(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "-", ":")
}

// rebindMAC authorizes a genuine WAN NIC replacement / VM migration. It is
// permission-gated + step-up-reauthed, requires the old and new MAC, a reason,
// and explicit confirmation, writes a full audit trail, updates the hardware
// inventory and re-issues a hardware-bound license for the NEW MAC (which
// supersedes — invalidates — the previous one). A MAC change never silently
// transfers a license: without this authorized action the appliance sits in a
// Hardware Binding Mismatch grace state.
func (b *Base) rebindMAC(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in rebindReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	in.NewMAC = normalizeMAC(in.NewMAC)
	in.OldMAC = normalizeMAC(in.OldMAC)
	if in.NewMAC == "" || in.Reason == "" || !in.Confirm {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "new_mac, reason and confirm are required")
		return
	}
	if b.Lic == nil {
		Fail(w, r, http.StatusServiceUnavailable, CodeInternal, "licensing not configured")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	var curMAC, tenantID, siteID, lifecycle string
	if err := b.DB.QueryRow(ctx,
		`SELECT COALESCE(wan_mac,''), COALESCE(tenant_id::text,''), COALESCE(site_id::text,''), lifecycle_state
           FROM appliances WHERE id=$1`, id).Scan(&curMAC, &tenantID, &siteID, &lifecycle); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}
	if tenantID == "" || siteID == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "appliance is not assigned; activate it first")
		return
	}
	// The old_mac must match what's on record (the mismatch that prompted rebind).
	if in.OldMAC != "" && normalizeMAC(curMAC) != in.OldMAC {
		Fail(w, r, http.StatusConflict, CodeBadRequest, "old_mac does not match the appliance's recorded WAN MAC")
		return
	}
	if in.ValidDays <= 0 {
		in.ValidDays = 365
	}
	if in.OfflineGraceDays <= 0 {
		in.OfflineGraceDays = 30
	}

	sess := auth.FromContext(r.Context())
	operatorID := ""
	if sess != nil {
		operatorID = sess.OperatorID
	}

	// Update inventory to the new MAC, then re-issue a hardware-bound license
	// (supersedes the previous one) and resolve any open mismatch alert.
	if _, err := b.DB.Exec(ctx, `UPDATE appliances SET wan_mac=$2, updated_at=now() WHERE id=$1`, id, in.NewMAC); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	doc, _, err := b.Lic.IssueForAppliance(ctx, tenantID, siteID, id, operatorID,
		time.Duration(in.ValidDays)*24*time.Hour, in.OfflineGraceDays)
	if err != nil {
		// Roll the MAC back so we never leave a mismatched inventory with no new license.
		_, _ = b.DB.Exec(ctx, `UPDATE appliances SET wan_mac=$2, updated_at=now() WHERE id=$1`, id, curMAC)
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "re-issue failed: "+err.Error())
		return
	}
	_, _ = b.DB.Exec(ctx, `UPDATE appliance_security_alerts SET status='resolved', resolved=true, acknowledged_by=NULLIF($2,'')::uuid, acknowledged_at=now()
	    WHERE appliance_id=$1 AND kind IN ('wan_mac_mismatch','hardware_mismatch') AND status='open'`, id, operatorID)
	audit.Op(ctx, b.DB, r, "appliance.wan_mac_rebound", "appliance", id, map[string]any{
		"_tenant_id": tenantID, "old_mac": curMAC, "new_mac": in.NewMAC, "reason": in.Reason, "new_license_id": doc.LicenseID})

	WriteJSON(w, http.StatusOK, map[string]any{
		"status": "rebound", "old_mac": curMAC, "new_mac": in.NewMAC, "license_id": doc.LicenseID,
	})
}

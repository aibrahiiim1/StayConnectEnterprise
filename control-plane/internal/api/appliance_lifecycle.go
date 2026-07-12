package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// Querier is satisfied by both *pgxpool.Pool and pgx.Tx (used for lifecycle
// events written inside or outside a transaction).
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// recordLifecycle appends an immutable lifecycle event.
func recordLifecycle(ctx context.Context, db Querier, applianceID, from, to, actor, srcIP, reason string) {
	_, _ = db.Exec(ctx, `
        INSERT INTO appliance_lifecycle_events (appliance_id, from_state, to_state, actor, source_ip, reason)
        VALUES ($1, NULLIF($2,''), $3, NULLIF($4,''), NULLIF($5,''), NULLIF($6,''))`,
		applianceID, from, to, actor, srcIP, reason)
}

// LifecycleRoutes are platform-only appliance enrollment/lifecycle operations,
// mounted under /cloud/v1/appliances. Each is gated by an explicit permission.
func (b *Base) LifecycleRoutes() http.Handler {
	r := chi.NewRouter()
	reauth := RequireReauth(b.Redis)
	r.With(auth.RequirePermission("platform.appliances.view")).Get("/pending", b.listPendingAppliances)
	r.With(auth.RequirePermission("platform.appliances.view")).Get("/security-alerts", b.listSecurityAlerts)
	// What signed assignment (if any) this appliance has been issued.
	r.With(auth.RequirePermission("platform.appliances.view")).Get("/{id}/assignment", b.PlatformAssignmentStatus)
	r.With(auth.RequirePermission("platform.appliances.assign"), reauth).Post("/{id}/assign", b.assignAppliance)
	// Reassignment to a DIFFERENT tenant/site is an elevated action: it needs
	// the reassign permission on top of a password step-up. Guest data is never
	// moved (it lives only on the appliance/site DB, never in the Cloud).
	r.With(auth.RequirePermission("platform.appliances.reassign"), reauth).Post("/{id}/reassign", b.assignAppliance)
	r.With(auth.RequirePermission("platform.appliances.claim")).Post("/{id}/claim", b.claimAppliance)
	r.With(auth.RequirePermission("platform.appliances.revoke"), reauth).Post("/{id}/revoke", b.revokeAppliance)
	r.With(auth.RequirePermission("platform.appliances.manage"), reauth).Post("/{id}/replace", b.replaceAppliance)
	r.With(auth.RequirePermission("platform.appliances.manage"), reauth).Post("/{id}/decommission", b.decommissionAppliance)
	r.With(auth.RequirePermission("platform.appliances.view")).Patch("/security-alerts/{id}", b.updateAlertStatus)
	return r
}

// TenantSupportRoutes are tenant-facing, own-appliance-only support actions.
// Tenants may REQUEST support/replacement/reassignment but never claim, license,
// revoke certs, or reach another tenant.
func (b *Base) TenantSupportRoutes() http.Handler {
	r := chi.NewRouter()
	r.With(auth.RequirePermission("tenant.appliances.support_request")).Post("/{id}/support-request", b.tenantRequest("support"))
	r.With(auth.RequirePermission("tenant.appliances.support_request")).Post("/{id}/replacement-request", b.tenantRequest("replacement"))
	r.With(auth.RequirePermission("tenant.appliances.support_request")).Post("/{id}/reassignment-request", b.tenantRequest("reassignment"))
	return r
}

func (b *Base) tenantRequest(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		var in struct {
			Note string `json:"note"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		ctx, cancel := DBCtx(r)
		defer cancel()
		// Own-appliance enforcement: the appliance must belong to the caller's
		// effective tenant (super admins may act on any).
		var applTenant string
		if err := b.DB.QueryRow(ctx, `SELECT COALESCE(tenant_id::text,'') FROM appliances WHERE id=$1`, id).Scan(&applTenant); err != nil {
			Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
			return
		}
		sess := auth.FromContext(r.Context())
		if sess != nil && !sess.IsSuperAdmin {
			if applTenant == "" || applTenant != sess.DefaultTenantID {
				Fail(w, r, http.StatusForbidden, CodeForbidden, "appliance not in your tenant")
				return
			}
		}
		audit.Op(ctx, b.DB, r, "tenant.appliance_"+kind+"_requested", "appliance", id, map[string]any{
			"_tenant_id": applTenant, "note": in.Note, "kind": kind})
		WriteJSON(w, http.StatusAccepted, map[string]any{"status": "requested", "kind": kind})
	}
}

func (b *Base) listPendingAppliances(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := DBCtx(r)
	defer cancel()
	// Everything AWAITING ASSIGNMENT, not merely awaiting claim. A claimed-but-
	// unassigned appliance has no tenant yet, so it appears in no tenant-scoped
	// list — without 'claimed' here it would vanish from the console after being
	// claimed and could never be assigned.
	rows, err := b.DB.Query(ctx, `
        SELECT id::text, serial, COALESCE(hardware_fingerprint,''), COALESCE(public_key,''),
               COALESCE(last_public_ip,''), COALESCE(version,''), lifecycle_state,
               COALESCE(first_seen_at,created_at), last_seen_at
          FROM appliances
         WHERE lifecycle_state IN ('pending_approval','pending_enrollment','claimed')
         ORDER BY created_at DESC LIMIT 200`)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, serial, hw, pk, ip, ver, state string
		var first, last any
		_ = rows.Scan(&id, &serial, &hw, &pk, &ip, &ver, &state, &first, &last)
		out = append(out, map[string]any{"id": id, "serial": serial, "hardware_fingerprint": hw,
			"public_key_fingerprint": fp(pk), "source_ip": ip, "version": ver, "state": state,
			"first_seen": first, "last_seen": last})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (b *Base) listSecurityAlerts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.DB.Query(ctx, `
        SELECT id::text, COALESCE(appliance_id::text,''), COALESCE(serial,''), kind,
               COALESCE(detail::text,'{}'), COALESCE(source_ip,''), resolved,
               COALESCE(status,'open'), created_at
          FROM appliance_security_alerts ORDER BY created_at DESC LIMIT 200`)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, appID, serial, kind, detail, ip, status string
		var resolved bool
		var at any
		_ = rows.Scan(&id, &appID, &serial, &kind, &detail, &ip, &resolved, &status, &at)
		out = append(out, map[string]any{"id": id, "appliance_id": appID, "serial": serial,
			"kind": kind, "detail": json.RawMessage(detail), "source_ip": ip,
			"resolved": resolved, "status": status, "at": at})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

type assignReq struct {
	TenantID string `json:"tenant_id"`
	SiteID   string `json:"site_id"`
	Reason   string `json:"reason"`
	Override bool   `json:"override_allocation"`
}

// claimAppliance moves an unclaimed/pending appliance to claimed (no tenant yet).
func (b *Base) claimAppliance(w http.ResponseWriter, r *http.Request) {
	b.transition(w, r, "claimed", "platform.appliances.claim")
}

// assignAppliance assigns tenant/site (with license-allocation check), records
// an assignment row + lifecycle event, and moves the appliance to 'assigned'.
func (b *Base) assignAppliance(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in assignReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.TenantID == "" || in.SiteID == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "tenant_id and site_id required")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	// site must belong to tenant
	var ok int
	if b.DB.QueryRow(ctx, `SELECT count(*) FROM sites WHERE id=$1 AND tenant_id=$2`, in.SiteID, in.TenantID).Scan(&ok); ok == 0 {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "site not in tenant")
		return
	}
	// license allocation check: appliance count in tenant vs limit.
	if !in.Override {
		limit, _ := GetIntLimit(ctx, b.DB, in.TenantID, "max_appliances")
		if limit > 0 {
			var cnt int64
			b.DB.QueryRow(ctx, `SELECT count(*) FROM appliances WHERE tenant_id=$1 AND lifecycle_state IN ('assigned','licensed','online','offline')`, in.TenantID).Scan(&cnt)
			if cnt >= limit {
				Fail(w, r, http.StatusConflict, "allocation_exceeded", "appliance allocation exceeded; upgrade subscription or override")
				return
			}
		}
	}
	var prevT, prevS, prevState string
	b.DB.QueryRow(ctx, `SELECT COALESCE(tenant_id::text,''),COALESCE(site_id::text,''),lifecycle_state FROM appliances WHERE id=$1`, id).Scan(&prevT, &prevS, &prevState)
	tag, err := b.DB.Exec(ctx, `UPDATE appliances SET tenant_id=$2, site_id=$3, lifecycle_state='assigned', status='enrolled', updated_at=now() WHERE id=$1`, id, in.TenantID, in.SiteID)
	if err != nil || tag.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}
	sess := auth.FromContext(r.Context())
	operatorID := ""
	if sess != nil {
		operatorID = sess.OperatorID
	}
	_, _ = b.DB.Exec(ctx, `
        INSERT INTO appliance_assignments (appliance_id, tenant_id, site_id, prev_tenant_id, prev_site_id, operator_id, source_ip, reason)
        VALUES ($1,$2,$3,NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,NULLIF($6,'')::uuid,$7,$8)`,
		id, in.TenantID, in.SiteID, prevT, prevS, operatorID, clientIPFromReq(r), in.Reason)
	recordLifecycle(ctx, b.DB, id, prevState, "assigned", emailOf(sess), clientIPFromReq(r), in.Reason)
	// Deliver the assignment to the appliance as a vendor-signed document it can
	// verify + persist as its local source of truth (replaces env/identity IDs).
	assignVersion := int64(0)
	if err := b.issueAssignment(ctx, id, "assigned"); err != nil {
		// Non-fatal to the operator action, but visible: the appliance will keep
		// polling and pick up the assignment once issuance succeeds.
		audit.Op(ctx, b.DB, r, "appliance.assignment_sign_failed", "appliance", id, map[string]any{"error": err.Error()})
	} else {
		_ = b.DB.QueryRow(ctx, `SELECT version FROM appliance_signed_assignments WHERE appliance_id=$1`, id).Scan(&assignVersion)
	}
	audit.Op(ctx, b.DB, r, "appliance.assigned", "appliance", id, map[string]any{
		"_tenant_id": in.TenantID, "site_id": in.SiteID, "prev_tenant_id": prevT, "reason": in.Reason,
		"assignment_version": assignVersion})
	WriteJSON(w, http.StatusOK, map[string]any{"status": "assigned", "tenant_id": in.TenantID, "site_id": in.SiteID,
		"assignment_version": assignVersion})
}

// revokeAppliance revokes an appliance: lifecycle=revoked, blocks future auth.
func (b *Base) revokeAppliance(w http.ResponseWriter, r *http.Request) {
	b.transition(w, r, "revoked", "platform.appliances.revoke")
}

func (b *Base) transition(w http.ResponseWriter, r *http.Request, to, _ string) {
	id := chi.URLParam(r, "id")
	var in struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	ctx, cancel := DBCtx(r)
	defer cancel()
	var prev string
	if err := b.DB.QueryRow(ctx, `SELECT lifecycle_state FROM appliances WHERE id=$1`, id).Scan(&prev); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}
	// Map the fine-grained lifecycle_state onto the coarse legacy operational
	// `status` column (constrained to pending/enrolled/online/offline/retired).
	// lifecycle_state remains the authoritative state.
	newStatus := "enrolled"
	if to == "revoked" || to == "decommissioned" {
		newStatus = "retired"
	}
	if _, err := b.DB.Exec(ctx, `UPDATE appliances SET lifecycle_state=$2, status=$3, updated_at=now() WHERE id=$1`, id, to, newStatus); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	sess := auth.FromContext(r.Context())
	recordLifecycle(ctx, b.DB, id, prev, to, emailOf(sess), clientIPFromReq(r), in.Reason)
	// Revocation/decommission is delivered as a signed 'revoked' assignment so the
	// appliance stops operating even if it comes back online later.
	if to == "revoked" || to == "decommissioned" {
		if err := b.issueAssignment(ctx, id, "revoked"); err != nil {
			audit.Op(ctx, b.DB, r, "appliance.assignment_sign_failed", "appliance", id, map[string]any{"error": err.Error()})
		}
	}
	audit.Op(ctx, b.DB, r, "appliance."+to, "appliance", id, map[string]any{"reason": in.Reason, "prev_state": prev})
	WriteJSON(w, http.StatusOK, map[string]any{"status": to, "previous": prev})
}

// --- helpers ---

func emailOf(s *auth.Session) string {
	if s == nil {
		return "system"
	}
	return s.Email
}

func clientIPFromReq(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return xff
	}
	return r.RemoteAddr
}

// replaceAppliance starts the replacement workflow for a failed/old box: it
// marks the outgoing appliance replacement_pending and mints a replacement-
// specific bootstrap token bound to the SAME tenant/site. Guest data is NOT
// touched — it lives only on the appliance/site DB and is restored locally via
// the secure local backup path, never carried through the Cloud.
func (b *Base) replaceAppliance(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		ExpectedSerial string `json:"expected_serial"`
		Reason         string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	ctx, cancel := DBCtx(r)
	defer cancel()
	var tenantID, siteID, prevState string
	if err := b.DB.QueryRow(ctx, `SELECT COALESCE(tenant_id::text,''), COALESCE(site_id::text,''), lifecycle_state FROM appliances WHERE id=$1`, id).Scan(&tenantID, &siteID, &prevState); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}
	if tenantID == "" || siteID == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "appliance must be assigned before replacement")
		return
	}
	// Mint a single-use replacement token (hashed at rest, shown once).
	plaintext, err := mintTokenPlaintext()
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "token mint failed")
		return
	}
	sum := sha256.Sum256([]byte(plaintext))
	sess := auth.FromContext(r.Context())
	var createdBy any
	if sess != nil {
		createdBy = sess.OperatorID
	}
	var expSerial any
	if in.ExpectedSerial != "" {
		expSerial = in.ExpectedSerial
	}
	var tokID string
	if err := b.DB.QueryRow(ctx, `
        INSERT INTO appliance_bootstrap_tokens (tenant_id, site_id, expected_serial, token_hash, token_hint, created_by, expires_at)
        VALUES ($1,$2,$3,$4,$5,$6, now() + interval '72 hours') RETURNING id`,
		tenantID, siteID, expSerial, sum[:], plaintext[len(plaintext)-4:], createdBy).Scan(&tokID); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "replacement token failed")
		return
	}
	_, _ = b.DB.Exec(ctx, `UPDATE appliances SET replacement_pending=true, updated_at=now() WHERE id=$1`, id)
	recordLifecycle(ctx, b.DB, id, prevState, prevState, emailOf(sess), clientIPFromReq(r), "replacement initiated: "+in.Reason)
	audit.Op(ctx, b.DB, r, "appliance.replacement_started", "appliance", id, map[string]any{
		"_tenant_id": tenantID, "site_id": siteID, "token_id": tokID, "reason": in.Reason})
	WriteJSON(w, http.StatusCreated, map[string]any{
		"status": "replacement_pending", "replacement_token": plaintext,
		"tenant_id": tenantID, "site_id": siteID,
		"note": "Enroll the new appliance with this token, then assign it to the same site. The old certificate/license must be revoked once the new box is online.",
	})
}

// decommissionAppliance permanently retires an appliance and revokes its
// active certificate. Audit + local data are preserved.
func (b *Base) decommissionAppliance(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	ctx, cancel := DBCtx(r)
	defer cancel()
	var prev string
	if err := b.DB.QueryRow(ctx, `SELECT lifecycle_state FROM appliances WHERE id=$1`, id).Scan(&prev); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}
	tx, _ := b.DB.Begin(ctx)
	defer tx.Rollback(ctx)
	_, _ = tx.Exec(ctx, `UPDATE appliances SET lifecycle_state='decommissioned', status='retired', current_cert_fingerprint=NULL, updated_at=now() WHERE id=$1`, id)
	// Revoke any active certificate (record the revocation for the mTLS check).
	_, _ = tx.Exec(ctx, `
        INSERT INTO appliance_certificate_revocations (certificate_id, appliance_id, fingerprint_sha256, reason)
        SELECT c.id, c.appliance_id, c.fingerprint_sha256, 'decommission'
          FROM appliance_certificates c WHERE c.appliance_id=$1 AND c.status='active'`, id)
	_, _ = tx.Exec(ctx, `UPDATE appliance_certificates SET status='revoked', revoked_at=now(), revocation_reason='decommission' WHERE appliance_id=$1 AND status='active'`, id)
	sess := auth.FromContext(r.Context())
	recordLifecycle(ctx, tx, id, prev, "decommissioned", emailOf(sess), clientIPFromReq(r), in.Reason)
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "commit failed")
		return
	}
	audit.Op(ctx, b.DB, r, "appliance.decommissioned", "appliance", id, map[string]any{"reason": in.Reason, "prev_state": prev})
	WriteJSON(w, http.StatusOK, map[string]any{"status": "decommissioned", "previous": prev})
}

// updateAlertStatus advances a security alert through its triage lifecycle.
func (b *Base) updateAlertStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	switch in.Status {
	case "open", "investigating", "acknowledged", "resolved", "false_positive":
	default:
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "invalid status")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	sess := auth.FromContext(r.Context())
	var actor any
	if sess != nil {
		actor = sess.OperatorID
	}
	resolved := in.Status == "resolved" || in.Status == "false_positive"
	tag, err := b.DB.Exec(ctx, `
        UPDATE appliance_security_alerts
           SET status=$2, resolved=$3, acknowledged_by=$4, acknowledged_at=now()
         WHERE id=$1`, id, in.Status, resolved, actor)
	if err != nil || tag.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "alert not found")
		return
	}
	audit.Op(ctx, b.DB, r, "security_alert.status_changed", "security_alert", id, map[string]any{"status": in.Status})
	WriteJSON(w, http.StatusOK, map[string]any{"status": in.Status})
}

// fp returns a short fingerprint of a public key string (never the key itself).
func fp(pk string) string {
	if len(pk) < 12 {
		return pk
	}
	return pk[:8] + "…" + pk[len(pk)-4:]
}

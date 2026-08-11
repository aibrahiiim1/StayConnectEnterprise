//go:build integration

package main

// THE PMS INTERFACE ADMIN SURFACE, over real HTTP against a real PostgreSQL 16.
//
// These run through the actual chi router with the actual session/RBAC middleware, so a handler that
// disagrees with its own schema, its own role table, or its own audit path fails here rather than on an
// appliance. Nothing touches a production database, an appliance or a PMS.
//
// The fixture publishes revision 1 while revision 2 exists, deliberately. Every assertion about "which
// Revision is live" would pass by accident if the newest were always the published one.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// testSecretKeyHex is a 32-byte AES key for the disposable database only. It never leaves this file and is
// not a credential for anything that exists.
const (
	testSecretKeyID  = "00000000-0000-4000-8000-0000000000aa"
	testSecretKeyHex = "1f8b0800000000000003fedcba98765432100123456789abcdef001122334455"
)

func (f *apiFixture) seedInterface(t *testing.T) (iface, rev1, rev2 string) {
	t.Helper()
	ctx := context.Background()
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,display_label,lifecycle_state)
		VALUES (gen_random_uuid(),$1,$2,'protel-fias',$3,'ACTIVE') RETURNING id::text`,
		f.tenant, f.site, fmt.Sprintf("PMS %d", time.Now().UnixNano())).Scan(&iface); err != nil {
		t.Fatalf("seed interface: %v", err)
	}
	mk := func(no int, cfg string) string {
		var id string
		if err := f.pool.QueryRow(ctx, `
			INSERT INTO iam_v2.pms_interface_revisions
			  (id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,folio_identity_strategy,
			   config,normalization_version)
			VALUES (gen_random_uuid(),$1,$2,$3::uuid,$4,'Europe/Berlin','UNIQUE_PER_STAY',$5::jsonb,1)
			RETURNING id::text`, f.tenant, f.site, iface, no, cfg).Scan(&id); err != nil {
			t.Fatalf("seed revision %d: %v", no, err)
		}
		return id
	}
	rev1 = mk(1, `{"host":"pms.local","port":5010,"password":"hunter2","nested":{"api_key":"sk-live-abc"}}`)
	rev2 = mk(2, `{"host":"pms.local","port":5011}`)
	if _, err := f.pool.Exec(ctx,
		`UPDATE iam_v2.pms_interfaces SET current_revision_id=$2::uuid WHERE id=$1::uuid`, iface, rev1); err != nil {
		t.Fatalf("publish revision 1: %v", err)
	}
	return iface, rev1, rev2
}

func (f *apiFixture) labelOf(t *testing.T, iface string) string {
	t.Helper()
	var label string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT display_label FROM iam_v2.pms_interfaces WHERE id=$1::uuid`, iface).Scan(&label); err != nil {
		t.Fatal(err)
	}
	return label
}

// The published Revision is the one the interface POINTS AT, not the newest. A property rolling back to an
// earlier configuration is the case that makes "highest revision_no" wrong — and it is exactly the case where
// showing the wrong answer sends an operator to debug configuration that is not running.
func TestIntegration_API_PublishedRevisionIsThePointedAtOneNotTheNewest(t *testing.T) {
	f := newAPI(t)
	iface, rev1, rev2 := f.seedInterface(t)

	code, body := f.do(t, http.MethodGet, "/pms-interfaces/"+iface+"/revisions", nil)
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	revs, _ := body["revisions"].([]any)
	if len(revs) != 2 {
		t.Fatalf("want 2 revisions, got %d", len(revs))
	}
	published := map[string]bool{}
	for _, r := range revs {
		m := r.(map[string]any)
		published[m["id"].(string)] = m["published"].(bool)
	}
	if !published[rev1] {
		t.Fatal("the published revision is not marked published")
	}
	if published[rev2] {
		t.Fatal("a newer, unpublished revision was marked published")
	}
}

// A Revision's config is operator-authored, so it will eventually contain a credential somebody pasted into
// the wrong field. An admin page is exactly where that becomes a screenshot in a support ticket.
func TestIntegration_API_RevisionConfigIsRedacted(t *testing.T) {
	f := newAPI(t)
	iface, _, _ := f.seedInterface(t)

	code, body := f.do(t, http.MethodGet, "/pms-interfaces/"+iface+"/revisions", nil)
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	raw, _ := json.Marshal(body)
	for _, secret := range []string{"hunter2", "sk-live-abc"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("the revision listing disclosed %q", secret)
		}
	}
	if !bytes.Contains(raw, []byte("[redacted]")) {
		t.Fatal("nothing was redacted, so the redaction never ran")
	}
	// Redacting everything would be useless: the operator still has to be able to read the configuration.
	if !bytes.Contains(raw, []byte("pms.local")) {
		t.Fatal("redaction removed the ordinary configuration too")
	}
}

// Publishing takes a step-up. It changes what every subsequent guest is resolved against, so an unattended or
// stolen session must not be enough on its own.
func TestIntegration_API_PublishingRequiresStepUp(t *testing.T) {
	f := newAPI(t)
	iface, rev1, rev2 := f.seedInterface(t)

	for _, pw := range []string{"", "not-the-password"} {
		code, _ := f.do(t, http.MethodPost, "/pms-interfaces/"+iface+"/publish", map[string]any{
			"revision_id": rev2, "expected_revision_id": rev1, "reason_code": "CONFIG_UPDATE", "password": pw,
		})
		if code != http.StatusUnauthorized {
			t.Fatalf("publishing with password %q returned %d, want 401", pw, code)
		}
	}
	var current string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT current_revision_id::text FROM iam_v2.pms_interfaces WHERE id=$1::uuid`, iface).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current != rev1 {
		t.Fatal("a refused publication changed the published revision anyway")
	}
}

// A reason code is required, because "why is this property on revision 7?" gets asked months later by someone
// who was not there.
func TestIntegration_API_PublishingRequiresAReason(t *testing.T) {
	f := newAPI(t)
	iface, rev1, rev2 := f.seedInterface(t)
	code, body := f.do(t, http.MethodPost, "/pms-interfaces/"+iface+"/publish", map[string]any{
		"revision_id": rev2, "expected_revision_id": rev1, "password": f.password,
	})
	if code != http.StatusBadRequest || body["error"] != "reason_required" {
		t.Fatalf("publishing without a reason returned %d %v", code, body)
	}
}

// THE OPTIMISTIC CONFLICT. Two operators open the form; one publishes; the second must be refused rather than
// silently reverting the first. The refusal carries what is published NOW, so the UI can show the difference
// instead of asking the operator to guess why they were refused.
func TestIntegration_API_ConcurrentPublicationIsRefusedNotOverwritten(t *testing.T) {
	f := newAPI(t)
	iface, rev1, rev2 := f.seedInterface(t)

	code, body := f.do(t, http.MethodPost, "/pms-interfaces/"+iface+"/publish", map[string]any{
		"revision_id": rev2, "expected_revision_id": rev1, "reason_code": "CONFIG_UPDATE", "password": f.password,
	})
	if code != http.StatusOK {
		t.Fatalf("the first publication failed: %d %v", code, body)
	}
	// the second operator still believes rev1 is live
	code, body = f.do(t, http.MethodPost, "/pms-interfaces/"+iface+"/publish", map[string]any{
		"revision_id": rev1, "expected_revision_id": rev1, "reason_code": "ROLLBACK", "password": f.password,
	})
	if code != http.StatusConflict {
		t.Fatalf("a stale publication returned %d, want 409", code)
	}
	if body["current_revision_id"] != rev2 {
		t.Fatalf("the conflict did not name what is actually published: %v", body)
	}
	var current string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT current_revision_id::text FROM iam_v2.pms_interfaces WHERE id=$1::uuid`, iface).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current != rev2 {
		t.Fatal("the refused publication reverted the first one anyway")
	}
}

// A successful publication is audited with both the new and the previous Revision, so the record answers
// "what changed" rather than merely "something changed".
func TestIntegration_API_PublicationIsAudited(t *testing.T) {
	f := newAPI(t)
	iface, rev1, rev2 := f.seedInterface(t)
	if code, body := f.do(t, http.MethodPost, "/pms-interfaces/"+iface+"/publish", map[string]any{
		"revision_id": rev2, "expected_revision_id": rev1, "reason_code": "CONFIG_UPDATE", "password": f.password,
	}); code != http.StatusOK {
		t.Fatalf("publish failed: %d %v", code, body)
	}
	var payload []byte
	if err := f.pool.QueryRow(context.Background(),
		`SELECT payload FROM public.audit_log WHERE action='pms_interface.revision_published' AND target_id=$1
		 ORDER BY id DESC LIMIT 1`, iface).Scan(&payload); err != nil {
		t.Fatalf("no audit row: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatal(err)
	}
	if m["revision_id"] != rev2 || m["previous_revision_id"] != rev1 || m["reason_code"] != "CONFIG_UPDATE" {
		t.Fatalf("the audit does not record what changed: %v", m)
	}
}

// A Revision belonging to ANOTHER interface must not be publishable: every subsequent resolution would be
// decided against configuration written for a different PMS.
func TestIntegration_API_ARevisionFromAnotherInterfaceCannotBePublished(t *testing.T) {
	f := newAPI(t)
	ifaceA, rev1A, _ := f.seedInterface(t)
	_, _, rev2B := f.seedInterface(t)

	code, body := f.do(t, http.MethodPost, "/pms-interfaces/"+ifaceA+"/publish", map[string]any{
		"revision_id": rev2B, "expected_revision_id": rev1A, "reason_code": "CONFIG_UPDATE", "password": f.password,
	})
	if code != http.StatusBadRequest || body["error"] != "revision_invalid" {
		t.Fatalf("publishing a foreign revision returned %d %v", code, body)
	}
}

// CROSS-SITE REFUSAL. Every read is scoped to this appliance's site, and another site's interface is
// indistinguishable from one that does not exist — a different answer would confirm what a neighbouring
// property runs.
func TestIntegration_API_AnotherSitesInterfaceIsNotFound(t *testing.T) {
	f := newAPI(t)
	var otherIface string
	if err := f.pool.QueryRow(context.Background(), `WITH
	  t AS (INSERT INTO public.tenants(id) VALUES (gen_random_uuid()) RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id) SELECT gen_random_uuid(), id FROM t RETURNING id, tenant_id),
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,display_label,lifecycle_state)
	         SELECT gen_random_uuid(), si.tenant_id, si.id, 'protel-fias','Neighbour PMS','ACTIVE' FROM si RETURNING id)
	SELECT (SELECT id FROM pi)::text`).Scan(&otherIface); err != nil {
		t.Fatalf("seed the other site: %v", err)
	}

	if code, _ := f.do(t, http.MethodGet, "/pms-interfaces/"+otherIface, nil); code != http.StatusNotFound {
		t.Fatalf("another site's interface returned %d, want 404", code)
	}
	// absent from the listing, not merely un-fetchable
	code, body := f.do(t, http.MethodGet, "/pms-interfaces", nil)
	if code != http.StatusOK {
		t.Fatalf("list status %d", code)
	}
	raw, _ := json.Marshal(body)
	if bytes.Contains(raw, []byte(otherIface)) || bytes.Contains(raw, []byte("Neighbour PMS")) {
		t.Fatal("the listing disclosed another site's interface")
	}
	// and its revisions and health are equally unreachable
	for _, p := range []string{"/pms-interfaces/" + otherIface + "/revisions", "/pms-interfaces/" + otherIface + "/health"} {
		_, b := f.do(t, http.MethodGet, p, nil)
		raw, _ := json.Marshal(b)
		if bytes.Contains(raw, []byte("Neighbour PMS")) {
			t.Fatalf("%s disclosed another site's interface", p)
		}
	}
}

// RBAC. A viewer sees the evidence and cannot act on it; the front desk is the same. Publishing and rotating
// belong to the role that owns the integration.
func TestIntegration_API_ReadOnlyRolesCannotPublishOrRotate(t *testing.T) {
	for _, role := range []string{"site_viewer", "front_office_operator"} {
		t.Run(role, func(t *testing.T) {
			f := newAPI(t, role)
			iface, rev1, rev2 := f.seedInterface(t)

			if code, _ := f.do(t, http.MethodGet, "/pms-interfaces", nil); code != http.StatusOK {
				t.Fatalf("%s cannot read the interface list: %d", role, code)
			}
			if code, _ := f.do(t, http.MethodPost, "/pms-interfaces/"+iface+"/publish", map[string]any{
				"revision_id": rev2, "expected_revision_id": rev1, "reason_code": "X", "password": f.password,
			}); code != http.StatusForbidden {
				t.Fatalf("%s was allowed to publish (%d)", role, code)
			}
			if code, _ := f.do(t, http.MethodPost, "/pms-interfaces/"+iface+"/secret", map[string]any{
				"secret": "s3cr3t", "reason_code": "X", "password": f.password,
			}); code != http.StatusForbidden {
				t.Fatalf("%s was allowed to rotate a credential (%d)", role, code)
			}
		})
	}
}

// THE CREDENTIAL IS WRITE-ONLY. There is no endpoint that returns it; this proves the surrounding surfaces do
// not leak it either — not the detail, not the listing, not the revision config, not the rotation's own
// response, and not the audit log.
func TestIntegration_API_ARotatedCredentialIsNeverReadableAgain(t *testing.T) {
	f := newAPI(t)
	iface, _, _ := f.seedInterface(t)
	t.Setenv("PMSD_SECRET_KEY_ID", testSecretKeyID)
	t.Setenv("PMSD_SECRET_KEY_HEX", testSecretKeyHex)

	const secret = "correct-horse-battery-staple"
	code, body := f.do(t, http.MethodPost, "/pms-interfaces/"+iface+"/secret", map[string]any{
		"secret": secret, "reason_code": "ROTATION", "password": f.password,
	})
	if code != http.StatusOK {
		t.Fatalf("rotation failed: %d %v", code, body)
	}
	if body["generation_no"] != float64(1) {
		t.Fatalf("first rotation produced generation %v", body["generation_no"])
	}
	if raw, _ := json.Marshal(body); bytes.Contains(raw, []byte(secret)) {
		t.Fatal("the rotation response echoed the credential")
	}

	for _, path := range []string{
		"/pms-interfaces", "/pms-interfaces/" + iface, "/pms-interfaces/" + iface + "/revisions",
	} {
		_, b := f.do(t, http.MethodGet, path, nil)
		if raw, _ := json.Marshal(b); bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("%s disclosed the credential", path)
		}
	}
	// the audit records that it happened, not what it was — and not a hash either, since a hash of a short
	// operator-chosen string is not much of a secret
	var payload []byte
	if err := f.pool.QueryRow(context.Background(),
		`SELECT payload FROM public.audit_log WHERE action='pms_interface.secret_rotated' AND target_id=$1
		 ORDER BY id DESC LIMIT 1`, iface).Scan(&payload); err != nil {
		t.Fatalf("no audit row: %v", err)
	}
	if bytes.Contains(payload, []byte(secret)) {
		t.Fatal("the audit log recorded the credential")
	}
	var ct []byte
	if err := f.pool.QueryRow(context.Background(),
		`SELECT ciphertext FROM iam_v2.pms_interface_secret_generations
		  WHERE pms_interface_id=$1::uuid AND superseded_at IS NULL`, iface).Scan(&ct); err != nil {
		t.Fatalf("no secret row: %v", err)
	}
	if bytes.Contains(ct, []byte(secret)) {
		t.Fatal("the credential was stored in the clear")
	}
}

// Rotating twice supersedes the first generation. Two live generations would make "which credential does the
// connector use" a question answered by an ORDER BY.
func TestIntegration_API_RotationSupersedesThePreviousGeneration(t *testing.T) {
	f := newAPI(t)
	iface, _, _ := f.seedInterface(t)
	t.Setenv("PMSD_SECRET_KEY_ID", testSecretKeyID)
	t.Setenv("PMSD_SECRET_KEY_HEX", testSecretKeyHex)

	for i := 1; i <= 2; i++ {
		code, body := f.do(t, http.MethodPost, "/pms-interfaces/"+iface+"/secret", map[string]any{
			"secret": fmt.Sprintf("secret-%d", i), "reason_code": "ROTATION", "password": f.password,
		})
		if code != http.StatusOK || body["generation_no"] != float64(i) {
			t.Fatalf("rotation %d: %d %v", i, code, body)
		}
	}
	var live int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*)::int FROM iam_v2.pms_interface_secret_generations
		  WHERE pms_interface_id=$1::uuid AND superseded_at IS NULL`, iface).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("%d live credential generations, want exactly 1", live)
	}
}

// Rotation also takes a step-up and a reason: it is the action that can silently disconnect a property.
func TestIntegration_API_RotationRequiresStepUpAndAReason(t *testing.T) {
	f := newAPI(t)
	iface, _, _ := f.seedInterface(t)
	t.Setenv("PMSD_SECRET_KEY_ID", testSecretKeyID)
	t.Setenv("PMSD_SECRET_KEY_HEX", testSecretKeyHex)

	if code, _ := f.do(t, http.MethodPost, "/pms-interfaces/"+iface+"/secret", map[string]any{
		"secret": "x", "reason_code": "ROTATION", "password": "wrong",
	}); code != http.StatusUnauthorized {
		t.Fatalf("rotation without a step-up returned %d, want 401", code)
	}
	if code, body := f.do(t, http.MethodPost, "/pms-interfaces/"+iface+"/secret", map[string]any{
		"secret": "x", "password": f.password,
	}); code != http.StatusBadRequest || body["error"] != "reason_required" {
		t.Fatalf("rotation without a reason returned %d %v", code, body)
	}
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*)::int FROM iam_v2.pms_interface_secret_generations WHERE pms_interface_id=$1::uuid`,
		iface).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("a refused rotation stored a credential anyway")
	}
}

// Without an encryption key the rotation is REFUSED, not stored in the clear. A credential written
// unencrypted "for now" is a credential written unencrypted forever, and nothing downstream would notice.
func TestIntegration_API_RotationWithoutAnEncryptionKeyIsRefused(t *testing.T) {
	f := newAPI(t)
	iface, _, _ := f.seedInterface(t)
	t.Setenv("PMSD_SECRET_KEY_ID", "")
	t.Setenv("PMSD_SECRET_KEY_HEX", "")

	code, body := f.do(t, http.MethodPost, "/pms-interfaces/"+iface+"/secret", map[string]any{
		"secret": "plaintext-please", "reason_code": "ROTATION", "password": f.password,
	})
	if code != http.StatusServiceUnavailable || body["error"] != "encryption_unavailable" {
		t.Fatalf("rotation without a key returned %d %v", code, body)
	}
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*)::int FROM iam_v2.pms_interface_secret_generations WHERE pms_interface_id=$1::uuid`,
		iface).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("a refused rotation stored a credential anyway")
	}
}

// Health is DERIVED. The runtime row carries facts; the four dimensions and the backlog are computed at read
// time, so nothing can keep saying "healthy" after it stops being true.
func TestIntegration_API_InterfaceHealthIsDerivedFromTheFacts(t *testing.T) {
	f := newAPI(t)
	iface, rev1, _ := f.seedInterface(t)
	ctx := context.Background()

	// CONNECTED is only a legal runtime state when the row also pins WHAT it connected with (pir_connected_pins).
	// Seeding it any other way would seed a state the appliance itself can never produce, and the test would be
	// proving the handler against a shape that does not occur.
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.pms_interface_runtime
		(tenant_id,site_id,pms_interface_id,runtime_generation,pinned_revision_id,credential_mode,
		 transport_status,continuity_status,sync_status,last_connected_at,last_heartbeat_at,last_valid_event_at)
		VALUES ($1,$2,$3::uuid,1,$4::uuid,'NONE','CONNECTED','CONTINUOUS','IN_SYNC',now(),now(),now())`,
		f.tenant, f.site, iface, rev1); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.stay_events
			(tenant_id,site_id,pms_interface_id,external_event_identity,event_type,payload,pms_timestamp_utc,
			 admission_kind,admission_runtime_generation,resync_generation,received_at,processing_status)
			VALUES ($1,$2,$3::uuid,$4,'GI','{}'::jsonb,now(),'LIVE',1,0,now() - interval '5 minutes','PENDING')`,
			f.tenant, f.site, iface, fmt.Sprintf("EV-P-%d-%d", time.Now().UnixNano(), i)); err != nil {
			t.Fatalf("seed pending event: %v", err)
		}
	}
	// Events are ADMITTED as PENDING and only the engine moves one to a terminal state — the schema enforces
	// that directly. So the review event is seeded the way the engine produces it: admitted, then moved.
	var review string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.stay_events
		(tenant_id,site_id,pms_interface_id,external_event_identity,event_type,payload,pms_timestamp_utc,
		 admission_kind,admission_runtime_generation,resync_generation,received_at)
		VALUES ($1,$2,$3::uuid,$4,'GI','{}'::jsonb,now(),'LIVE',1,0,now()) RETURNING id::text`,
		f.tenant, f.site, iface, fmt.Sprintf("EV-R-%d", time.Now().UnixNano())).Scan(&review); err != nil {
		t.Fatalf("seed review event: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.stay_events
		SET processing_status='MANUAL_REVIEW', processed_at=now(), review_code='AMBIGUOUS' WHERE id=$1::uuid`,
		review); err != nil {
		t.Fatalf("move the review event: %v", err)
	}

	code, body := f.do(t, http.MethodGet, "/pms-interfaces/"+iface+"/health", nil)
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	h, _ := body["health"].(map[string]any)
	if h["transport_status"] != "CONNECTED" || h["continuity_status"] != "CONTINUOUS" || h["sync_status"] != "IN_SYNC" {
		t.Fatalf("the health dimensions do not reflect the runtime row: %v", h)
	}
	if h["pending_events"] != float64(2) {
		t.Fatalf("pending backlog = %v, want 2", h["pending_events"])
	}
	if h["review_events"] != float64(1) {
		t.Fatalf("review backlog = %v, want 1", h["review_events"])
	}
	// The oldest pending timestamp is what separates "a busy morning" from "a stuck processor", and it is the
	// number an operator actually acts on.
	if h["oldest_pending_at"] == nil {
		t.Fatal("the backlog does not say how old the oldest waiting event is")
	}
}

// An interface with NO runtime row has never connected. That is a real state and must read as UNKNOWN rather
// than fail, because "we have never heard from this PMS" is exactly what an operator needs to be told.
func TestIntegration_API_AnInterfaceThatNeverConnectedReportsUnknown(t *testing.T) {
	f := newAPI(t)
	iface, _, _ := f.seedInterface(t)
	code, body := f.do(t, http.MethodGet, "/pms-interfaces/"+iface+"/health", nil)
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	h, _ := body["health"].(map[string]any)
	if h["transport_status"] != "UNKNOWN" || h["continuity_status"] != "UNKNOWN" || h["sync_status"] != "UNKNOWN" {
		t.Fatalf("a never-connected interface did not report UNKNOWN: %v", h)
	}
}

// Guest-network routing shows both what IS mapped and what is NOT. A guest network with no mapping resolves
// against nothing, and that absence is invisible in a list of what exists.
func TestIntegration_API_RoutingNamesTheUnmappedGuestNetworksToo(t *testing.T) {
	f := newAPI(t)
	iface, _, _ := f.seedInterface(t)
	ctx := context.Background()

	mkNet := func(name string) string {
		var id string
		if err := f.pool.QueryRow(ctx, `
			INSERT INTO public.guest_networks(id,tenant_id,site_id,name,enabled)
			VALUES (gen_random_uuid(),$1,$2,$3,true) RETURNING id::text`, f.tenant, f.site, name).Scan(&id); err != nil {
			t.Fatalf("seed guest network %q: %v", name, err)
		}
		return id
	}
	mapped := mkNet("Guest VLAN 10")
	orphan := mkNet("Conference VLAN 20")
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.guest_network_pms_map
		(tenant_id,site_id,guest_network_id,pms_interface_id,is_default,routing_mode)
		VALUES ($1,$2,$3::uuid,$4::uuid,true,'MAPPED')`, f.tenant, f.site, mapped, iface); err != nil {
		t.Fatalf("seed mapping: %v", err)
	}

	code, body := f.do(t, http.MethodGet, "/pms-routing", nil)
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	routes, _ := body["routes"].([]any)
	if len(routes) != 1 || routes[0].(map[string]any)["guest_network_id"] != mapped {
		t.Fatalf("the mapped route is wrong: %v", routes)
	}
	if routes[0].(map[string]any)["pms_interface_label"] != f.labelOf(t, iface) {
		t.Fatalf("the route does not name the interface an operator would recognise: %v", routes[0])
	}
	unmapped, _ := body["unmapped_guest_networks"].([]any)
	if len(unmapped) != 1 || unmapped[0].(map[string]any)["guest_network_id"] != orphan {
		t.Fatalf("the unmapped guest network was not surfaced: %v", unmapped)
	}
}

// Source conflicts name BOTH interfaces by their operator-facing labels. A conflict rendered as two UUIDs is
// a conflict nobody resolves.
func TestIntegration_API_SourceConflictsNameBothInterfaces(t *testing.T) {
	f := newAPI(t)
	ifaceA, _, _ := f.seedInterface(t)
	ifaceB, _, _ := f.seedInterface(t)
	// The conflict is symmetric, so the schema fixes an orientation (psc_order: interface_a < interface_b) to
	// stop the same pair being recorded twice in opposite directions.
	if ifaceB < ifaceA {
		ifaceA, ifaceB = ifaceB, ifaceA
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO iam_v2.pms_source_conflicts
		(id,tenant_id,site_id,interface_a,interface_b,severity,resolution)
		VALUES (gen_random_uuid(),$1,$2,$3::uuid,$4::uuid,'HIGH','UNRESOLVED')`,
		f.tenant, f.site, ifaceA, ifaceB); err != nil {
		t.Fatalf("seed conflict: %v", err)
	}
	code, body := f.do(t, http.MethodGet, "/pms-source-conflicts", nil)
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	conflicts, _ := body["conflicts"].([]any)
	if len(conflicts) != 1 {
		t.Fatalf("want 1 conflict, got %d", len(conflicts))
	}
	c := conflicts[0].(map[string]any)
	if c["interface_a_label"] != f.labelOf(t, ifaceA) || c["interface_b_label"] != f.labelOf(t, ifaceB) {
		t.Fatalf("the conflict does not name both interfaces: %v", c)
	}
	if c["severity"] != "HIGH" {
		t.Fatalf("severity lost: %v", c)
	}
}

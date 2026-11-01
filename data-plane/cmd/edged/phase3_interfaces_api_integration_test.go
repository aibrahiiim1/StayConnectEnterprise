//go:build integration

package main

// The fixture publishes revision 1 while revision 2 exists, so "which Revision is live" cannot pass by
// accident when the newest is always the published one.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// testSecretKeyHex is a 32-byte AES key for the disposable test database only.
const (
	testSecretKeyID  = "00000000-0000-4000-8000-0000000000aa"
	testSecretKeyHex = "1f8b0800000000000003fedcba98765432100123456789abcdef001122334455"
)

func (f *apiFixture) seedInterface(t *testing.T) (iface, rev1, rev2 string) {
	t.Helper()
	ctx := context.Background()
	// A site with a PMS interface offers Room sign-in in its Sign-in methods, as a configured hotel does; readiness now
	// reports a site whose method is off as off (scd refuses every guest there), so the fixture states it.
	if _, err := f.pool.Exec(ctx, `UPDATE public.tenants SET auth_methods = COALESCE(auth_methods,'{}'::jsonb) || '{"pms":{"enabled":true,"mode":"either"}}'::jsonb
		WHERE id=$1::uuid`, f.tenant); err != nil {
		t.Fatalf("switch room sign-in on: %v", err)
	}
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
			  (id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,posting_target_model,
			   config,normalization_version)
			VALUES (gen_random_uuid(),$1,$2,$3::uuid,$4,'Europe/Berlin','RESERVATION',$5::jsonb,1)
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

// The published Revision is the one the interface points at, not the highest revision_no: after a rollback the
// wrong answer would send an operator to debug configuration that is not running.
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

// Revision config is operator-authored and may contain a credential pasted into the wrong field; it is redacted.
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
	// Non-secret configuration must stay readable.
	if !bytes.Contains(raw, []byte("pms.local")) {
		t.Fatal("redaction removed the ordinary configuration too")
	}
}

// Publishing takes a step-up: it changes what every later guest resolves against, so a stolen session is not enough.
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

// A reason code is required so the record can answer later why a property is on a given revision.
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

// Optimistic conflict: a second operator is refused rather than silently reverting the first, and the refusal
// carries what is published now so the UI can show the difference.
func TestIntegration_API_ConcurrentPublicationIsRefusedNotOverwritten(t *testing.T) {
	f := newAPI(t)
	iface, rev1, rev2 := f.seedInterface(t)

	code, body := f.do(t, http.MethodPost, "/pms-interfaces/"+iface+"/publish", map[string]any{
		"revision_id": rev2, "expected_revision_id": rev1, "reason_code": "CONFIG_UPDATE", "password": f.password,
	})
	if code != http.StatusOK {
		t.Fatalf("the first publication failed: %d %v", code, body)
	}
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

// A publication is audited with both the new and previous Revision, so the record says what changed.
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
		 ORDER BY ts DESC LIMIT 1`, iface).Scan(&payload); err != nil {
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

// A Revision of another interface must not be publishable: resolution would use configuration written for a different PMS.
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

// Reads are scoped to this site, and another site's interface is indistinguishable from a missing one, so a
// different answer cannot confirm what a neighbouring property runs.
func TestIntegration_API_AnotherSitesInterfaceIsNotFound(t *testing.T) {
	f := newAPI(t)
	var otherIface string
	if err := f.pool.QueryRow(context.Background(), `WITH
	  t AS (INSERT INTO public.tenants(id,slug,name) SELECT g, g::text, 't' FROM gen_random_uuid() g RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id,code,name) SELECT g, t.id, g::text, 's' FROM t, gen_random_uuid() g RETURNING id, tenant_id),
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,display_label,lifecycle_state)
	         SELECT gen_random_uuid(), si.tenant_id, si.id, 'protel-fias','Neighbour PMS','ACTIVE' FROM si RETURNING id)
	SELECT (SELECT id FROM pi)::text`).Scan(&otherIface); err != nil {
		t.Fatalf("seed the other site: %v", err)
	}

	if code, _ := f.do(t, http.MethodGet, "/pms-interfaces/"+otherIface, nil); code != http.StatusNotFound {
		t.Fatalf("another site's interface returned %d, want 404", code)
	}
	code, body := f.do(t, http.MethodGet, "/pms-interfaces", nil)
	if code != http.StatusOK {
		t.Fatalf("list status %d", code)
	}
	raw, _ := json.Marshal(body)
	if bytes.Contains(raw, []byte(otherIface)) || bytes.Contains(raw, []byte("Neighbour PMS")) {
		t.Fatal("the listing disclosed another site's interface")
	}
	for _, p := range []string{"/pms-interfaces/" + otherIface + "/revisions", "/pms-interfaces/" + otherIface + "/health"} {
		_, b := f.do(t, http.MethodGet, p, nil)
		raw, _ := json.Marshal(b)
		if bytes.Contains(raw, []byte("Neighbour PMS")) {
			t.Fatalf("%s disclosed another site's interface", p)
		}
	}
}

// A viewer or the front desk sees the evidence but cannot act; publishing and rotating belong to the integration's owner role.
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

// The credential is write-only: the detail, listing, revision config, rotation response and audit log must not leak it.
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
	// The audit records that it happened, not the value or a hash (a hash of a short string is guessable).
	var payload []byte
	if err := f.pool.QueryRow(context.Background(),
		`SELECT payload FROM public.audit_log WHERE action='pms_interface.secret_rotated' AND target_id=$1
		 ORDER BY ts DESC LIMIT 1`, iface).Scan(&payload); err != nil {
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

// Rotating twice supersedes the first generation; two live ones would leave the credential choice to an ORDER BY.
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

// Rotation takes a step-up and a reason: it can silently disconnect a property.
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

// Without an encryption key the rotation is refused, never stored in the clear.
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

// Health is derived at read time from the runtime row's facts, so nothing keeps saying "healthy" after it stops being true.
func TestIntegration_API_InterfaceHealthIsDerivedFromTheFacts(t *testing.T) {
	f := newAPI(t)
	iface, rev1, _ := f.seedInterface(t)
	ctx := context.Background()

	// CONNECTED is legal only when the row also pins what it connected with (pir_connected_pins).
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.pms_interface_runtime
		(tenant_id,site_id,pms_interface_id,runtime_generation,pinned_revision_id,credential_mode,
		 transport_status,continuity_status,sync_status,last_connected_at,last_heartbeat_at,last_valid_event_at)
		VALUES ($1,$2,$3::uuid,1,$4::uuid,'NONE','CONNECTED','CONTINUOUS','IN_SYNC',now(),now(),now())`,
		f.tenant, f.site, iface, rev1); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO iam_v2.stay_events
			(tenant_id,site_id,pms_interface_id,external_event_identity,event_type,payload,pms_timestamp_utc,
			 admission_kind,admission_runtime_generation,resync_generation,received_at,processing_status)
			VALUES ($1,$2,$3::uuid,$4,'GI','{}'::jsonb,now(),'LIVE',1,0,now() - interval '5 minutes','PENDING')`,
				f.tenant, f.site, iface, fmt.Sprintf("EV-P-%d-%d", time.Now().UnixNano(), i))
			return err
		}); err != nil {
			t.Fatalf("seed pending event: %v", err)
		}
	}
	// The schema lets only the engine move a PENDING event to a terminal state, so seed it admitted, then moved.
	var review string
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.stay_events
		(tenant_id,site_id,pms_interface_id,external_event_identity,event_type,payload,pms_timestamp_utc,
		 admission_kind,admission_runtime_generation,resync_generation,received_at)
		VALUES ($1,$2,$3::uuid,$4,'GI','{}'::jsonb,now(),'LIVE',1,0,now()) RETURNING id::text`,
			f.tenant, f.site, iface, fmt.Sprintf("EV-R-%d", time.Now().UnixNano())).Scan(&review); err != nil {
			return fmt.Errorf("seed: %w", err)
		}
		_, err := tx.Exec(ctx, `UPDATE iam_v2.stay_events
		SET processing_status='MANUAL_REVIEW', processed_at=now(), review_code='AMBIGUOUS' WHERE id=$1::uuid`,
			review)
		return err
	}); err != nil {
		t.Fatalf("seed/move the review event: %v", err)
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
	// The oldest pending timestamp separates a busy morning from a stuck processor.
	if h["oldest_pending_at"] == nil {
		t.Fatal("the backlog does not say how old the oldest waiting event is")
	}
}

// An interface with no runtime row has never connected and must read as UNKNOWN rather than fail.
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

// Guest-network routing shows what is not mapped too: an unmapped network resolves against nothing, invisibly.
func TestIntegration_API_RoutingNamesTheUnmappedGuestNetworksToo(t *testing.T) {
	f := newAPI(t)
	iface, _, _ := f.seedInterface(t)
	ctx := context.Background()

	mkNet := func(name string) string {
		id, _ := seedGuestNetwork(t, f.pool, f.tenant, f.site, name, "10.251.0.0/24")
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

// Source conflicts name both interfaces by their operator-facing labels, not UUIDs.
func TestIntegration_API_SourceConflictsNameBothInterfaces(t *testing.T) {
	f := newAPI(t)
	ifaceA, _, _ := f.seedInterface(t)
	ifaceB, _, _ := f.seedInterface(t)
	// psc_order (interface_a < interface_b) stops the same pair being recorded twice in opposite directions.
	if ifaceB < ifaceA {
		ifaceA, ifaceB = ifaceB, ifaceA
	}
	if err := controlled(context.Background(), f.pool, []string{"source_conflict"}, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `INSERT INTO iam_v2.pms_source_conflicts
		(id,tenant_id,site_id,interface_a,interface_b,severity,resolution)
		VALUES (gen_random_uuid(),$1,$2,$3::uuid,$4::uuid,'HIGH','UNRESOLVED')`,
			f.tenant, f.site, ifaceA, ifaceB)
		return err
	}); err != nil {
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

// Command acceptance exercises the Central contract (docs/CENTRAL_CONTROL_PLANE.md) end to end against a
// running ctrlapi, playing both the operator and the appliance:
//
//	register (token-less, self-signed) -> WAITING -> Activate (inline customer + site, licence) -> CSR
//	auto-signed -> ACTIVATED -> mTLS hello + certificate-only assignment -> licence suspend/resume/set/
//	expiry/grace (appliance never cut off) -> rebind keeps terms -> offline files -> roles -> move (same
//	customer only; licence carried with the same terms) -> retire (normal + emergency) -> delete -> a
//	retired identity cannot register again -> replacement retires the old box through its signed ack ->
//	an appliance reporting holds_customer_id (identity reset without factory reset) activates only for that
//	customer, online and offline; plus replay/body-tamper rejection, step-up, audit, /metrics.
//
// It creates its own fixtures (serials ACC-*, customers "Acceptance *") and needs:
//
//	BASE   http://127.0.0.1:8080           the ctrlapi HTTP listener
//	MTLS   https://127.0.0.1:9443          the mTLS appliance listener
//	PA / PASS                              a platform_admin (see: ctrlapi seed-admin)
//	PG_EXEC "docker exec -i sc-central-pg" how to run psql against Central's database (for time travel)
//
// Prints PASS:<n> / FAIL:<n> and exits non-zero on any failure.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/activation"
	"github.com/stayconnect/enterprise/control-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/control-plane/internal/assignment"
)

var (
	base   = env("BASE", "http://127.0.0.1:8080")
	mtls   = env("MTLS", "https://127.0.0.1:9443")
	pass   = env("PASS", "AcceptTest!2026")
	pa     = env("PA", "accept-pa@stayconnect.local")
	pgExec = strings.Fields(env("PG_EXEC", "docker exec -i sc-central-pg"))
	run    = fmt.Sprintf("%d", time.Now().Unix()%100000)
	npass  = 0
	nfail  = 0
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func ok(m string, a ...any)  { fmt.Printf("  PASS: "+m+"\n", a...); npass++ }
func bad(m string, a ...any) { fmt.Printf("  FAIL: "+m+"\n", a...); nfail++ }
func check(cond bool, m string, a ...any) {
	if cond {
		ok(m, a...)
	} else {
		bad(m, a...)
	}
}

func psql(q string) string {
	args := append(append([]string{}, pgExec[1:]...), "psql", "-U", env("PGUSER", "stayconnect"), "-d", env("PGDB", "stayconnect"), "-tAc", q)
	out, _ := exec.Command(pgExec[0], args...).Output()
	return strings.TrimSpace(string(out))
}

// appliance is a simulated appliance: an identity key and, once issued, a client certificate.
type appliance struct {
	serial  string
	id      string
	priv    ed25519.PrivateKey
	tlsPriv ed25519.PrivateKey
	certPEM string
	caPEM   string
	holds   string // holds_customer_id it reports: the customer whose data it still holds ("" = none)
}

func main() {
	op := login(pa)
	reauth(op)

	// ---------------- registration -> WAITING ----------------
	a1 := newAppliance("ACC-A1-" + run)
	code, body := a1.register()
	check(code == 200 && a1.id != "", "registration is token-less and self-signed (HTTP %d)", code)
	check(a1.registerForged() == 401, "a registration signed by another key is refused")
	row := get(op, "/cloud/v1/appliances/"+a1.id)
	check(field(row, "activation") == "waiting", "a registered appliance is WAITING")
	check(strings.Contains(get(op, "/cloud/v1/appliances?activation=waiting"), a1.serial), "it is listed under activation=waiting")
	check(strings.Contains(get(op, "/cloud/v1/overview"), `"waiting_activation"`), "the overview lists it as needing attention")
	_ = body

	// A CSR sent while waiting is queued, not signed.
	a1.tlsPriv = newKey()
	code, _ = a1.signed("POST", "/v1/appliance/csr", jb(map[string]any{"csr_pem": makeCSR(a1.tlsPriv, a1.id)}))
	check(code == 202, "a waiting appliance's CSR is queued (HTTP %d)", code)

	// ---------------- step-up ----------------
	op2 := login(pa) // fresh session: no step-up yet
	code, body = do(op2, "POST", "/cloud/v1/appliances/"+a1.id+"/activate", activateBody("", "Acceptance Hotels "+run, "", "Main "+run))
	check(code == 403 && strings.Contains(body, "reauth_required"), "activation without step-up is refused (HTTP %d)", code)

	// ---------------- activate ----------------
	code, body = do(op, "POST", "/cloud/v1/appliances/"+a1.id+"/activate", activateBody("", "Acceptance Hotels "+run, "", "Main "+run))
	check(code == 200, "activate with an inline customer and site (HTTP %d %s)", code, trunc(body))
	custID, siteID := field(body, "customer_id"), field(body, "site_id")
	check(custID != "" && siteID != "", "the appliance is bound to the new customer and site")
	check(field(body, "activation") == "activated", "the waiting CSR was signed by the activation, so it is ACTIVATED (%s)", field(body, "activation"))
	check(sub(body, "license", "state") == "active", "its licence is active")
	code, _ = do(op, "POST", "/cloud/v1/appliances/"+a1.id+"/activate", activateBody(custID, "", siteID, ""))
	check(code == 409, "activating again is refused: activate is WAITING-only (HTTP %d)", code)

	// ---------------- certificate + mTLS ----------------
	fetch := a1.get("/v1/appliance/certificate")
	a1.certPEM, a1.caPEM = field(fetch, "certificate_pem"), field(fetch, "ca_chain")
	check(a1.certPEM != "" && certHasURI(a1.certPEM, a1.id), "the appliance collects a certificate bound to its id")
	code, body = a1.mtls("GET", "/v1/appliance/hello", true)
	check(code == 200 && field(body, "appliance_id") == a1.id, "hello over mTLS (HTTP %d)", code)
	code, body = a1.mtls("GET", "/v1/appliance/assignment", false)
	check(code == 200 && field(body, "state") == "assigned" && field(body, "site_id") == siteID,
		"the signed assignment is served on the certificate-only channel (HTTP %d)", code)
	code, _ = a1.mtls("GET", "/v1/appliance/assignment-registry", true)
	check(code == 200 || code == 204, "assignment registry reachable over mTLS (HTTP %d)", code)

	// ---------------- replay / tamper ----------------
	tok := a1.token("GET", "/v1/appliance/hello", nil)
	c1, c2 := rawSigned("GET", "/v1/appliance/hello", tok, nil), rawSigned("GET", "/v1/appliance/hello", tok, nil)
	check(c1 == 200 && c2 == 401, "a replayed signed request is refused (%d then %d)", c1, c2)
	tok = a1.token("POST", "/v1/appliance/csr", []byte(`{}`))
	check(rawSigned("POST", "/v1/appliance/csr", tok, []byte(`{"csr_pem":"tampered"}`)) == 401, "a body-modified signed request is refused")

	// ---------------- licence operations never cut the appliance off (F4) ----------------
	licID := sub(get(op, "/cloud/v1/appliances/"+a1.id), "license", "id")
	code, body = do(op, "POST", "/cloud/v1/licenses/"+licID+"/suspend", jb(map[string]string{"reason": "acceptance"}))
	check(code == 200 && field(body, "state") == "suspended", "suspend issues a signed suspended licence (HTTP %d)", code)
	check(psql("SELECT lifecycle_state FROM appliances WHERE id='"+a1.id+"'") == "assigned", "suspension does not touch the appliance lifecycle")
	code, lic := a1.signed("GET", "/v1/appliance/license", nil)
	check(code == 200 && strings.Contains(lic, `"envelope"`), "a suspended appliance still fetches its licence (HTTP %d)", code)
	licID = field(body, "id")
	code, body = do(op, "POST", "/cloud/v1/licenses/"+licID+"/resume", jb(map[string]string{"reason": "acceptance"}))
	check(code == 200 && field(body, "state") == "active", "resume (HTTP %d)", code)
	code, body = do(op, "POST", "/cloud/v1/appliances/"+a1.id+"/license",
		jb(map[string]any{"max_concurrent_online_guests": 150, "valid_days": 200, "grace_period_days": 10, "reason": "new terms"}))
	check(code == 201 && num(body, "max_concurrent_online_guests") == 150, "set licence issues new terms (HTTP %d)", code)
	ver := num(body, "license_version")
	check(ver >= 4, "every change is a new version (now v%d)", ver)
	licID = field(body, "id")

	psql("UPDATE licenses SET valid_until=now()+interval '10 days' WHERE id='" + licID + "'")
	check(sub(get(op, "/cloud/v1/appliances/"+a1.id), "license", "state") == "expiring", "≤30 days left is EXPIRING, computed on read")
	psql("UPDATE licenses SET valid_until=now()-interval '2 days' WHERE id='" + licID + "'")
	check(sub(get(op, "/cloud/v1/appliances/"+a1.id), "license", "state") == "grace", "past valid_until inside grace is GRACE")
	psql("UPDATE licenses SET valid_until=now()-interval '20 days' WHERE id='" + licID + "'")
	check(sub(get(op, "/cloud/v1/appliances/"+a1.id), "license", "state") == "expired", "past grace is EXPIRED")
	check(strings.Contains(get(op, "/cloud/v1/licenses?state=expired"), licID), "the licence list filters by derived state")
	code, body = do(op, "POST", "/cloud/v1/appliances/"+a1.id+"/license",
		jb(map[string]any{"max_concurrent_online_guests": 150, "valid_days": 365, "grace_period_days": 10, "reason": "renew"}))
	check(code == 201 && field(body, "state") == "active", "renewal (HTTP %d)", code)
	licID = field(body, "id")
	until := field(body, "valid_until")

	// ---------------- rebind keeps the terms (F8) ----------------
	code, _ = do(op, "POST", "/cloud/v1/appliances/"+a1.id+"/rebind-wan-mac", jb(map[string]any{"reason": "NIC swap", "new_mac": "02:00:00:aa:bb:99"}))
	check(code == 200, "rebind WAN MAC (HTTP %d)", code)
	det := get(op, "/cloud/v1/appliances/"+a1.id)
	newLic := sub(det, "license", "id")
	check(newLic != licID, "rebind re-signs the licence (new version)")
	nl := get(op, "/cloud/v1/licenses?q="+a1.serial)
	check(strings.Contains(nl, `"max_concurrent_online_guests":150`) && strings.Contains(nl, until),
		"rebind keeps cap and valid_until (no unlimited/365-day reset)")
	licID = newLic

	// ---------------- offline licence is the appliance's own (F10) ----------------
	code, body = do(op, "POST", "/cloud/v1/appliances/"+a1.id+"/offline-license", jb(map[string]any{}))
	if code == 503 {
		ok("offline licence file: vendor key not configured on this ctrlapi (503) — skipped")
	} else {
		_, own := a1.signed("GET", "/v1/appliance/license", nil)
		sig := sub(own, "envelope", "signature")
		check(code == 201 && sig != "" && strings.Contains(body, sig) && strings.Contains(body, `"license":`),
			"the offline licence file carries THIS appliance's current signed licence (HTTP %d)", code)
	}

	// ---------------- customer roles ----------------
	custAdmin := "acc-admin-" + run + "@example.test"
	reauth(op)
	code, _ = do(op, "POST", "/cloud/v1/customers/"+custID+"/users", jb(map[string]any{"email": custAdmin, "password": pass, "role": "tenant_admin"}))
	check(code == 201, "platform creates a customer admin (HTTP %d)", code)
	ta := login(custAdmin)
	who := get(ta, "/v1/auth/whoami")
	check(field(who, "customer_id") == custID && !strings.Contains(who, "fleet.view"), "whoami names the customer and grants no fleet view")
	a2 := newAppliance("ACC-A2-" + run)
	a2.register()
	check(!strings.Contains(get(ta, "/cloud/v1/appliances"), a2.serial), "a customer never sees waiting appliances of the fleet")
	check(strings.Contains(get(ta, "/cloud/v1/appliances"), a1.serial), "a customer sees its own appliance")
	reauth(ta)
	code, _ = do(ta, "POST", "/cloud/v1/appliances/"+a2.id+"/activate", activateBody(custID, "", siteID, ""))
	check(code == 403, "a customer admin cannot activate (HTTP %d)", code)
	code, _ = do(ta, "POST", "/cloud/v1/licenses/"+licID+"/suspend", jb(map[string]string{"reason": "x"}))
	check(code == 403, "a customer admin cannot touch licences (HTTP %d)", code)
	code, body = do(ta, "POST", "/cloud/v1/customers/"+custID+"/sites", jb(map[string]any{"name": "Annex " + run, "timezone": "Africa/Cairo"}))
	check(code == 201, "a customer admin manages its own sites (HTTP %d)", code)
	annex := field(body, "id")
	code, _ = do(ta, "PATCH", "/cloud/v1/customers/"+custID, jb(map[string]any{"name": "Renamed"}))
	check(code == 403, "a customer admin cannot rename the customer (HTTP %d)", code)
	other := field(post(op, "/cloud/v1/customers", jb(map[string]any{"name": "Acceptance Other " + run}), 201), "id")
	code, _ = do(ta, "POST", "/cloud/v1/customers/"+other+"/sites", jb(map[string]any{"name": "X", "timezone": "UTC"}))
	check(code == 403, "a customer admin cannot add sites to another customer (HTTP %d)", code)
	code, _ = do(ta, "GET", "/cloud/v1/customers/"+other, nil)
	check(code == 404, "another customer is invisible (HTTP %d)", code)
	code, _ = do(ta, "GET", "/cloud/v1/trust", nil)
	check(code == 403, "trust & keys are platform-only (HTTP %d)", code)
	code, body = do(ta, "GET", "/cloud/v1/audit", nil)
	check(code == 200 && strings.Contains(body, "site.created") && !strings.Contains(body, other),
		"a customer's audit log is its own")

	sup := "acc-support-" + run + "@example.test"
	fresh := login(pa)
	code, body = do(fresh, "POST", "/cloud/v1/team", jb(map[string]any{"email": "acc-nosu-" + run + "@example.test", "password": pass, "role": "platform_support"}))
	check(code == 403 && strings.Contains(body, "reauth_required"), "a Team change without step-up is refused (HTTP %d)", code)
	reauth(op)
	post(op, "/cloud/v1/team", jb(map[string]any{"email": sup, "password": pass, "role": "platform_support"}), 201)
	sp := login(sup)
	check(strings.Contains(get(sp, "/cloud/v1/appliances"), a2.serial), "platform_support reads the fleet")
	reauth(sp)
	code, _ = do(sp, "POST", "/cloud/v1/appliances/"+a2.id+"/activate", activateBody(custID, "", siteID, ""))
	check(code == 403, "platform_support cannot activate (HTTP %d)", code)
	code, _ = do(sp, "PATCH", "/cloud/v1/security-alerts/00000000-0000-0000-0000-000000000000", jb(map[string]string{"status": "resolved"}))
	check(code == 403, "security-alert triage needs security.manage, not view (HTTP %d)", code)

	// ---------------- move ----------------
	otherSite := field(post(op, "/cloud/v1/customers/"+other+"/sites", jb(map[string]any{"name": "Other " + run, "timezone": "UTC"}), 201), "id")
	code, body = do(op, "POST", "/cloud/v1/appliances/"+a1.id+"/move", jb(map[string]any{"customer_id": other, "site_id": otherSite, "reason": "sold"}))
	check(code == 409 && field(body, "error") == "cross_customer_move" && strings.Contains(field(body, "message"), "factory-reset"),
		"a move to another customer is refused with an operator sentence (HTTP %d %s)", code, trunc(body))
	check(field(get(op, "/cloud/v1/appliances/"+a1.id), "customer_id") == custID, "the refused move changed nothing")
	before := get(op, "/cloud/v1/appliances/"+a1.id)
	code, body = do(op, "POST", "/cloud/v1/appliances/"+a1.id+"/move", jb(map[string]any{"customer_id": custID, "site_id": annex, "reason": "moved in-house"}))
	check(code == 200 && field(body, "site_id") == annex && sub(body, "license", "state") == "active",
		"a same-customer move keeps a valid licence (re-issued for the new site) (HTTP %d)", code)
	moved := get(op, "/cloud/v1/licenses?q="+a1.serial+"&state=active")
	check(strings.Contains(moved, `"max_concurrent_online_guests":150`) && strings.Contains(moved, until) &&
		strings.Contains(moved, `"site_id":"`+annex+`"`),
		"the moved licence keeps cap and valid_until and is bound to the new site")
	check(subNum(body, "license", "license_version") == subNum(before, "license", "license_version")+1,
		"the moved licence is the next version (v%d -> v%d)", subNum(before, "license", "license_version"), subNum(body, "license", "license_version"))
	code, body = a1.mtls("GET", "/v1/appliance/assignment", false)
	check(code == 200 && field(body, "site_id") == annex, "the appliance receives a newly signed assignment")

	// ---------------- site/customer delete guards (F3) ----------------
	code, _ = do(op, "DELETE", "/cloud/v1/sites/"+annex, jb(map[string]any{"confirm": "Annex " + run, "reason": "x"}))
	check(code == 409, "a site with an appliance cannot be deleted — 409, not 500 (HTTP %d)", code)
	code, _ = do(op, "DELETE", "/cloud/v1/sites/"+siteID, jb(map[string]any{"confirm": "Main " + run, "reason": "empty now"}))
	check(code == 204, "an empty site deletes (HTTP %d)", code)
	code, _ = do(op, "DELETE", "/cloud/v1/customers/"+custID, jb(map[string]any{"confirm": "Acceptance Hotels " + run, "reason": "x"}))
	check(code == 409, "a customer with sites cannot be deleted (HTTP %d)", code)

	// ---------------- retire ----------------
	code, body = do(op, "POST", "/cloud/v1/appliances/"+a1.id+"/retire", jb(map[string]any{"reason": "end of life"}))
	check(code == 200 && field(body, "activation") == "retiring", "retire starts two-phase delivery: RETIRING (HTTP %d)", code)
	check(sub(body, "license", "state") == "revoked", "the licence is revoked at once")
	code, body = a1.mtls("GET", "/v1/appliance/assignment", false)
	check(code == 200 && field(body, "state") == "decommissioned", "the appliance receives the signed terminal assignment")
	code, _ = do(op, "DELETE", "/cloud/v1/appliances/"+a1.id, jb(map[string]any{"confirm_serial": a1.serial, "reason": "x"}))
	check(code == 409, "an appliance being retired cannot be deleted (HTTP %d)", code)
	code, body = do(op, "POST", "/cloud/v1/appliances/"+a1.id+"/retire", jb(map[string]any{"reason": "no ack", "emergency": true, "confirm_serial": a1.serial}))
	check(code == 200 && field(body, "activation") == "retired", "an emergency retire completes at once (HTTP %d)", code)
	code, _ = a1.signed("GET", "/v1/appliance/hello", nil)
	check(code == 403, "a retired identity is refused (HTTP %d)", code)
	code, _ = a1.mtls("GET", "/v1/appliance/hello", true)
	check(code != 200, "its certificate no longer opens mTLS (HTTP %d)", code)
	code, _ = do(op, "DELETE", "/cloud/v1/appliances/"+a1.id, jb(map[string]any{"confirm_serial": a1.serial, "reason": "gone"}))
	check(code == 204, "a retired appliance's record deletes (HTTP %d)", code)
	code, _ = a1.signed("GET", "/v1/appliance/hello", nil)
	check(code == 401, "a deleted appliance learns it is orphaned from hello (HTTP %d)", code)
	code, body = a1.register()
	check(code == 403 && field(body, "error") == "identity_retired",
		"a retired identity cannot register again after its record is deleted (HTTP %d)", code)
	a1b := &appliance{serial: a1.serial, priv: newKey()}
	code, _ = a1b.register()
	check(code == 200 && field(get(op, "/cloud/v1/appliances/"+a1b.id), "activation") == "waiting",
		"the same hardware with a new identity (factory reset) registers as WAITING (HTTP %d)", code)
	check(strings.Contains(get(op, "/cloud/v1/audit?appliance_id="+a1.id), "appliance.deleted"), "audit history outlives the appliance")

	// ---------------- replacement: the old box is retired through its signed acknowledgment ----------------
	code, body = do(op, "POST", "/cloud/v1/appliances/"+a2.id+"/activate", activateBody(custID, "", annex, ""))
	check(code == 200, "activate A2 at the annex (HTTP %d)", code)
	a2.tlsPriv = newKey()
	a2.signed("POST", "/v1/appliance/csr", jb(map[string]any{"csr_pem": makeCSR(a2.tlsPriv, a2.id)}))
	fetch = a2.get("/v1/appliance/certificate")
	a2.certPEM, a2.caPEM = field(fetch, "certificate_pem"), field(fetch, "ca_chain")
	check(a2.certPEM != "", "A2 holds a certificate")
	code, _ = do(op, "POST", "/cloud/v1/appliances/"+a2.id+"/replace", jb(map[string]any{"reason": "failing disk"}))
	check(code == 200, "mark A2 for replacement (HTTP %d)", code)
	a3 := newAppliance("ACC-A3-" + run)
	a3.register()
	code, _ = do(op, "POST", "/cloud/v1/appliances/"+a3.id+"/activate", activateBody(custID, "", annex, ""))
	check(code == 200, "activate the new hardware at the same site (HTTP %d)", code)
	old := get(op, "/cloud/v1/appliances/"+a2.id)
	check(field(old, "activation") == "retiring" && sub(old, "retirement", "state") == "terminal_delivery_pending",
		"activating the replacement starts the old appliance's acknowledged retirement (%s)", field(old, "activation"))
	check(sub(old, "license", "state") == "revoked", "the old appliance's licence is revoked at once")
	check(sub(old, "replacement", "replaced_by") == a3.id && sub(get(op, "/cloud/v1/appliances/"+a3.id), "replacement", "replaces") == a2.id,
		"replacement links are appliance ids")
	code, body = a2.mtls("GET", "/v1/appliance/hello", true)
	check(code == 200, "the old appliance's credentials stay valid until it acknowledges (HTTP %d)", code)
	code, body = a2.mtls("GET", "/v1/appliance/assignment", false)
	check(code == 200 && field(body, "state") == "decommissioned", "the old appliance fetches its signed retirement (HTTP %d)", code)
	var doc assignment.Document
	_ = json.Unmarshal([]byte(body), &doc)
	ack := &assignment.Ack{ApplianceID: a2.id, Version: doc.Version, TerminalState: doc.State,
		Fingerprint: assignment.DocFingerprint(&doc), AdoptedAt: time.Now().Unix()}
	assignment.SignAck(a2.priv, ack)
	code, body = a2.mtlsBody("POST", "/v1/appliance/assignment/ack", jb(ack))
	check(code == 200 && field(body, "status") == "credential_revoked", "the old appliance acknowledges (HTTP %d %s)", code, trunc(body))
	old = get(op, "/cloud/v1/appliances/"+a2.id)
	check(field(old, "activation") == "retired", "after the ack the old appliance is RETIRED (%s)", field(old, "activation"))
	code, _ = a2.mtls("GET", "/v1/appliance/hello", true)
	check(code != 200, "and only now are its credentials revoked (HTTP %d)", code)

	// ---------------- offline first activation ----------------
	a4 := newAppliance("ACC-A4-" + run)
	req := &activation.Request{SchemaVersion: activation.SchemaVersion, RequestID: "req-" + run, Serial: a4.serial,
		PublicKey: b64(a4.priv.Public().(ed25519.PublicKey)), WANMAC: "02:00:00:00:04:01", CreatedAt: time.Now().Unix(), Nonce: "n-" + run}
	activation.SignRequest(a4.priv, req)
	code, body = do(op, "POST", "/cloud/v1/offline-activation/requests", jb(req))
	if code == 503 {
		ok("offline activation: vendor key not configured on this ctrlapi (503) — skipped")
	} else {
		a4.id = field(body, "id")
		check(code == 200 && field(body, "activation") == "waiting", "an imported activation request makes a WAITING appliance (HTTP %d)", code)
		do(op, "POST", "/cloud/v1/appliances/"+a4.id+"/activate", activateBody(custID, "", annex, ""))
		code, body = do(op, "POST", "/cloud/v1/appliances/"+a4.id+"/offline-activation-package", jb(map[string]any{}))
		if code == 503 {
			ok("activation package: CTRLAPI_APPLIANCE_BASE not set on this ctrlapi (503) — skipped")
		} else {
			check(code == 201 && strings.Contains(body, `"assignment"`) && strings.Contains(body, `"request_nonce":"n-`+run+`"`),
				"the activation package carries the signed assignment bound to the request (HTTP %d)", code)
		}
	}

	// ---------------- an appliance that still holds a customer's data (identity reset, no factory reset) ----------------
	// It registers with a new key but reports holds_customer_id inside its signed body. Central keeps it
	// WAITING, shows whose data it holds, and activates it for that customer or not at all.
	code, body = do(op, "POST", "/cloud/v1/customers", jb(map[string]any{"name": "Acceptance Other " + run}))
	custY := field(body, "id")
	check((code == 200 || code == 201) && custY != "", "a second customer exists (HTTP %d)", code)
	a5 := newAppliance("ACC-A5-" + run)
	a5.holds = custID
	code, _ = a5.register()
	row = get(op, "/cloud/v1/appliances/"+a5.id)
	check(code == 200 && field(row, "activation") == "waiting", "an identity-reset appliance registers as WAITING (HTTP %d)", code)
	custName := field(get(op, "/cloud/v1/customers/"+custID), "name")
	check(field(row, "holds_customer_id") == custID && custName != "" && field(row, "holds_customer_name") == custName,
		"its row names the customer whose data it still holds (%s / %s)", field(row, "holds_customer_id"), field(row, "holds_customer_name"))
	// The field is inside the signed body: stripping it from a request signed with it is refused.
	signedBody := a5.registerBody()
	tok5 := claimsToken(a5.priv, applianceauth.KeyID(a5.priv.Public().(ed25519.PublicKey)), "POST", "/v1/appliances/register", signedBody)
	a5.holds = ""
	code, _ = rawSignedBody("POST", "/v1/appliances/register", tok5, a5.registerBody())
	check(code == 401, "holds_customer_id cannot be stripped from a signed registration (HTTP %d)", code)
	check(field(get(op, "/cloud/v1/appliances/"+a5.id), "holds_customer_id") == custID, "and the stored value is unchanged")
	a5.holds = custID
	code, body = do(op, "POST", "/cloud/v1/appliances/"+a5.id+"/activate", activateBody(custY, "", "", "Other site "+run))
	check(code == 409 && field(body, "error") == "holds_other_customer_data" &&
		field(body, "message") == "This appliance still holds another customer's data. Factory-reset it before activating it for a different customer.",
		"activating it for ANOTHER customer is refused with an operator sentence (HTTP %d %s)", code, trunc(body))
	code, body = do(op, "POST", "/cloud/v1/appliances/"+a5.id+"/activate", activateBody("", "Acceptance Inline "+run, "", "Inline "+run))
	check(code == 409 && field(body, "error") == "holds_other_customer_data", "or for a customer created inline (HTTP %d)", code)
	check(field(get(op, "/cloud/v1/appliances/"+a5.id), "activation") == "waiting" &&
		!strings.Contains(get(op, "/cloud/v1/customers?q=Acceptance+Inline+"+run), "Acceptance Inline "+run),
		"a refused activation changed nothing (still WAITING, no inline customer created)")
	// A customer deleted from Central is still another customer's data.
	a6 := newAppliance("ACC-A6-" + run)
	a6.holds = "00000000-0000-4000-8000-00000000dead"
	a6.register()
	row = get(op, "/cloud/v1/appliances/"+a6.id)
	check(field(row, "holds_customer_id") == a6.holds && field(row, "holds_customer_name") == "",
		"an appliance can hold a customer Central no longer knows (name is null)")
	code, body = do(op, "POST", "/cloud/v1/appliances/"+a6.id+"/activate", activateBody(custID, "", annex, ""))
	check(code == 409 && field(body, "error") == "holds_other_customer_data", "and it is refused for any existing customer (HTTP %d)", code)
	// Registering again replaces the value; after a real factory reset the appliance reports none.
	a6.holds = ""
	code, _ = a6.register()
	check(code == 200 && field(get(op, "/cloud/v1/appliances/"+a6.id), "holds_customer_id") == "",
		"a registration without holds_customer_id clears it (HTTP %d)", code)
	code, _ = do(op, "POST", "/cloud/v1/appliances/"+a6.id+"/activate", activateBody(custY, "", "", "Y main "+run))
	check(code == 200, "an appliance holding nothing activates for any customer (HTTP %d)", code)
	// Same customer: legitimate breakglass recovery.
	code, body = do(op, "POST", "/cloud/v1/appliances/"+a5.id+"/activate", activateBody(custID, "", annex, ""))
	check(code == 200 && field(body, "customer_id") == custID, "the appliance activates for the customer whose data it holds (HTTP %d %s)", code, trunc(body))

	// Offline: the activation request carries the same signed field and the import applies the same rule.
	a7 := newAppliance("ACC-A7-" + run)
	req7 := &activation.Request{SchemaVersion: activation.SchemaVersion, RequestID: "req7-" + run, Serial: a7.serial,
		PublicKey: b64(a7.priv.Public().(ed25519.PublicKey)), WANMAC: "02:00:00:00:07:01", CreatedAt: time.Now().Unix(),
		Nonce: "n7-" + run, HoldsCustomerID: custID}
	activation.SignRequest(a7.priv, req7)
	tampered := *req7
	tampered.HoldsCustomerID = ""
	code, _ = do(op, "POST", "/cloud/v1/offline-activation/requests", jb(tampered))
	if code == 503 {
		ok("offline holds_customer_id: vendor key not configured on this ctrlapi (503) — skipped")
	} else {
		check(code == 400, "an activation request whose holds_customer_id was stripped is refused (HTTP %d)", code)
		code, body = do(op, "POST", "/cloud/v1/offline-activation/requests", jb(req7))
		a7.id = field(body, "id")
		check(code == 200 && field(body, "holds_customer_id") == custID, "an imported request records the customer it holds (HTTP %d)", code)
		code, body = do(op, "POST", "/cloud/v1/appliances/"+a7.id+"/activate", activateBody(custY, "", "", "Y offline "+run))
		check(code == 409 && field(body, "error") == "holds_other_customer_data", "and it cannot be activated for another customer (HTTP %d)", code)
		code, _ = do(op, "POST", "/cloud/v1/appliances/"+a7.id+"/activate", activateBody(custID, "", annex, ""))
		check(code == 200, "but can for the customer it holds (HTTP %d)", code)
	}

	// ---------------- system ----------------
	check(strings.Contains(get(op, "/cloud/v1/trust"), `"assignment_keys"`), "trust & keys")
	check(strings.Contains(get(op, "/cloud/v1/audit?action=appliance.activated"), "appliance.activated"), "platform audit filters by action")
	req2, _ := http.NewRequest("GET", base+"/metrics", nil)
	req2.Header.Set("X-Real-IP", "203.0.113.5")
	if resp, err := http.DefaultClient.Do(req2); err == nil {
		check(resp.StatusCode == 404, "/metrics refuses a proxied request (HTTP %d)", resp.StatusCode)
		resp.Body.Close()
	}
	for _, gone := range []string{"/v1/appliances/enroll", "/cloud/v1/appliances-admin/pending", "/v1/tenants", "/cloud/v1/commercial-plans", "/v1/checkout/x", "/cloud/v1/appliance-bootstrap-tokens"} {
		code, _ = do(op, "GET", gone, nil)
		check(code == 404 || code == 405, "%s is gone (HTTP %d)", gone, code)
	}

	fmt.Printf("\nCENTRAL ACCEPTANCE: PASS=%d FAIL=%d\n", npass, nfail)
	if nfail > 0 {
		os.Exit(1)
	}
}

func activateBody(customerID, newCustomer, siteID, newSite string) []byte {
	b := map[string]any{"license": map[string]any{"max_concurrent_online_guests": 100, "valid_days": 365, "grace_period_days": 7}}
	if customerID != "" {
		b["customer_id"] = customerID
	} else {
		b["new_customer"] = map[string]any{"name": newCustomer}
	}
	if siteID != "" {
		b["site_id"] = siteID
	} else {
		b["new_site"] = map[string]any{"name": newSite, "timezone": "Africa/Cairo", "country": "EG"}
	}
	return jb(b)
}

// ---------------- operator helpers ----------------

func login(email string) *http.Client {
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	if code, body := do(cl, "POST", "/v1/auth/login", jb(map[string]string{"email": email, "password": pass})); code != 200 {
		bad("login %s: HTTP %d %s", email, code, trunc(body))
	}
	return cl
}

func reauth(cl *http.Client) {
	if code, body := do(cl, "POST", "/v1/auth/reauth", jb(map[string]string{"password": pass})); code != 200 {
		bad("reauth: HTTP %d %s", code, trunc(body))
	}
}

func do(cl *http.Client, method, path string, body []byte) (int, string) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, _ := http.NewRequest(method, base+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cl.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func get(cl *http.Client, path string) string {
	code, body := do(cl, "GET", path, nil)
	if code != 200 {
		bad("GET %s: HTTP %d %s", path, code, trunc(body))
	}
	return body
}

func post(cl *http.Client, path string, body []byte, want int) string {
	code, out := do(cl, "POST", path, body)
	if code != want {
		bad("POST %s: want %d got %d %s", path, want, code, trunc(out))
	}
	return out
}

// ---------------- appliance helpers ----------------

func newKey() ed25519.PrivateKey { _, p, _ := ed25519.GenerateKey(rand.Reader); return p }

func newAppliance(serial string) *appliance { return &appliance{serial: serial, priv: newKey()} }

func (a *appliance) registerBody() []byte {
	pub := a.priv.Public().(ed25519.PublicKey)
	m := map[string]any{"serial": a.serial, "wan_mac": "02:00:00:aa:bb:01", "lan_mac": "02:00:00:aa:bb:02",
		"hardware_fingerprint": "HWF-" + a.serial, "hostname": "acc", "model": "acceptance", "public_key": b64(pub)}
	if a.holds != "" {
		m["holds_customer_id"] = a.holds
	}
	return jb(m)
}

func (a *appliance) registerWith(signer ed25519.PrivateKey) (int, string) {
	body := a.registerBody()
	spub := signer.Public().(ed25519.PublicKey)
	tok := claimsToken(signer, applianceauth.KeyID(spub), "POST", "/v1/appliances/register", body)
	return rawSignedBody("POST", "/v1/appliances/register", tok, body)
}

func (a *appliance) register() (int, string) {
	code, body := a.registerWith(a.priv)
	a.id = field(body, "appliance_id")
	return code, body
}

func (a *appliance) registerForged() int { code, _ := a.registerWith(newKey()); return code }

func (a *appliance) token(method, path string, body []byte) string {
	return claimsToken(a.priv, a.id, method, path, body)
}

func (a *appliance) signed(method, path string, body []byte) (int, string) {
	return rawSignedBody(method, path, a.token(method, path, body), body)
}

func (a *appliance) get(path string) string { _, b := a.signed("GET", path, nil); return b }

// mtls calls the mTLS listener with the appliance's client certificate; signed adds the request JWT.
func (a *appliance) mtls(method, path string, signed bool) (int, string) {
	keyDER, _ := x509.MarshalPKCS8PrivateKey(a.tlsPriv)
	pair, err := tls.X509KeyPair([]byte(a.certPEM), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		return 0, err.Error()
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(a.caPEM))
	cl := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: pool}}}
	req, _ := http.NewRequest(method, mtls+path, nil)
	if signed {
		req.Header.Set("Authorization", "Bearer "+a.token(method, path, nil))
	}
	resp, err := cl.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// mtlsBody is mtls for a signed request with a body.
func (a *appliance) mtlsBody(method, path string, body []byte) (int, string) {
	keyDER, _ := x509.MarshalPKCS8PrivateKey(a.tlsPriv)
	pair, err := tls.X509KeyPair([]byte(a.certPEM), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		return 0, err.Error()
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(a.caPEM))
	cl := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: pool}}}
	req, _ := http.NewRequest(method, mtls+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token(method, path, body))
	resp, err := cl.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func claimsToken(priv ed25519.PrivateKey, iss, method, path string, body []byte) string {
	pub := priv.Public().(ed25519.PublicKey)
	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	now := time.Now().UTC()
	t, _ := applianceauth.Encode(priv, applianceauth.Claims{Iss: iss, Kid: applianceauth.KeyID(pub), Iat: now.Unix(),
		Exp: now.Add(30 * time.Second).Unix(), Jti: fmt.Sprintf("%x", nonce), Aud: applianceauth.Audience,
		Mth: method, Pth: path, Bsh: applianceauth.BodyHash(body), Ver: "acceptance"})
	return t
}

func rawSigned(method, path, tok string, body []byte) int {
	code, _ := rawSignedBody(method, path, tok, body)
	return code
}

func rawSignedBody(method, path, tok string, body []byte) (int, string) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, _ := http.NewRequest(method, base+path, r)
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func makeCSR(priv ed25519.PrivateKey, cn string) string {
	der, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}, priv)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

func certHasURI(certPEM, appID string) bool {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return false
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	for _, u := range c.URIs {
		if strings.TrimPrefix(u.Path, "/") == appID || strings.HasSuffix(u.String(), appID) {
			return true
		}
	}
	return false
}

// ---------------- JSON helpers ----------------

func jb(v any) []byte { b, _ := json.Marshal(v); return b }

func decode(s string) map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

func field(s, k string) string {
	if v, ok := decode(s)[k].(string); ok {
		return v
	}
	return ""
}

func sub(s, k1, k2 string) string {
	if m, ok := decode(s)[k1].(map[string]any); ok {
		if v, ok := m[k2].(string); ok {
			return v
		}
	}
	return ""
}

func num(s, k string) int {
	if v, ok := decode(s)[k].(float64); ok {
		return int(v)
	}
	return -1
}

func subNum(s, k1, k2 string) int {
	if m, ok := decode(s)[k1].(map[string]any); ok {
		if v, ok := m[k2].(float64); ok {
			return int(v)
		}
	}
	return -1
}

func b64(pub ed25519.PublicKey) string { return base64.RawStdEncoding.EncodeToString(pub) }

func trunc(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

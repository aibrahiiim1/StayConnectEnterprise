//go:build integration

package main

// Poison tests for iam_v2.activate_session_enforcement, which moves a Session from PENDING_ENFORCEMENT to active (read
// downstream as "authorized AND accounted"). They call the operation directly with fabricated and mismatched claims and
// require the database to refuse, so no caller can produce an active Session metered from a nonexistent baseline.
// Needs PHASE3_TEST_DSN; skipped otherwise.

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
)

type activationFixture struct {
	f       *authFixture
	pool    *pgxpool.Pool
	session string
	device  string
	bridge  string
	minor   int
}

func (a *activationFixture) activate(t *testing.T, bridge string, minor int, epoch int64) (string, error) {
	t.Helper()
	var outcome string
	err := a.pool.QueryRow(context.Background(),
		`SELECT iam_v2.activate_session_enforcement($1,$2,$3::uuid,$4,$5,$6)`,
		a.f.tenant, a.f.site, a.session, bridge, minor, epoch).Scan(&outcome)
	return outcome, err
}

func (a *activationFixture) registerOrigin(t *testing.T, session, device, bridge string, minor int, epoch int64) {
	t.Helper()
	if _, err := a.pool.Exec(context.Background(), `
		SELECT iam_v2.register_class_origin($1,$2,$3::uuid,$4::uuid,$5,$6,$7,0,0,now())`,
		a.f.tenant, a.f.site, session, device, bridge, minor, epoch); err != nil {
		t.Fatalf("register origin: %v", err)
	}
}

func (a *activationFixture) state(t *testing.T) string {
	t.Helper()
	var st string
	if err := a.pool.QueryRow(context.Background(),
		`SELECT state FROM iam_v2.sessions WHERE id=$1::uuid`, a.session).Scan(&st); err != nil {
		t.Fatalf("read session state: %v", err)
	}
	return st
}

// newActivationFixture grants a real Phase-3 session with no enforcement owner running, leaving it PENDING_ENFORCEMENT.
// The grant uses a short deadline because it can never be confirmed and the handler waits out its whole budget.
func newActivationFixture(t *testing.T) *activationFixture {
	t.Helper()
	f := newAuthFixture(t)

	// A request id of its own per fixture: a constant one made resolution idempotent across runs, so a reused database could run the suite only once.
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", mustUUID(t, f.pool)))
	if res.Outcome != outcomeVerified {
		t.Fatalf("the fixture could not resolve: %+v", res)
	}
	raw, _ := json.Marshal(map[string]any{
		"auth_context_id":     res.AuthContextID,
		"package_revision_id": f.pkgRev,
		"device":              map[string]string{"ip": f.net.guestIP, "mac": f.net.mac},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)).WithContext(ctx)
	f.p3.grantHandler(httptest.NewRecorder(), req)

	var session, device, bridge, ip string
	err := f.pool.QueryRow(context.Background(), `
		SELECT s.id::text, s.device_id::text, COALESCE(s.ingress_interface,''), host(s.ip)
		  FROM iam_v2.sessions s
		 WHERE s.tenant_id=$1 AND s.site_id=$2 AND s.state='PENDING_ENFORCEMENT' AND s.ended IS NULL
		 ORDER BY s.started DESC LIMIT 1`, f.tenant, f.site).Scan(&session, &device, &bridge, &ip)
	if err != nil {
		t.Fatalf("no pending session to activate: %v", err)
	}
	minor, ok := shape.MinorForIP(net.ParseIP(ip))
	if !ok {
		t.Fatalf("unshapeable fixture address %s", ip)
	}
	return &activationFixture{f: f, pool: f.pool, session: session, device: device, bridge: bridge, minor: minor}
}

// No origin at all: a caller did the kernel work but never registered the accounting baseline.
func TestActivationRefusedWithoutARegisteredAccountingOrigin(t *testing.T) {
	a := newActivationFixture(t)
	_, err := a.activate(t, a.bridge, a.minor, 1)
	if err == nil {
		t.Fatal("a Session was promoted to active with no accounting origin: its traffic would be billed from a baseline that does not exist")
	}
	if !strings.Contains(err.Error(), "ENFORCE_NOT_ACCOUNTABLE") {
		t.Fatalf("refused for the wrong reason: %v", err)
	}
	if st := a.state(t); st != "PENDING_ENFORCEMENT" {
		t.Fatalf("session state = %s after a refused activation", st)
	}
}

// A fabricated origin belonging to another session does not make this one accountable. The class-origin writer now refuses
// the registration itself (the origin must describe the session it names); both that and the non-activatability are asserted.
func TestActivationRefusedWhenTheOriginBelongsToAnotherSession(t *testing.T) {
	a := newActivationFixture(t)
	otherSession := a.f.secondSessionOnSameClass(t)

	out := a.tryRegisterOrigin(t, otherSession, a.device, a.bridge, a.minor, 1)
	if out == "" {
		t.Fatal("an origin naming another session was accepted for this class")
	}
	if !strings.Contains(out, "ACCT_SOURCE_MISMATCH") {
		t.Fatalf("the forged origin was refused for the wrong reason: %s", out)
	}

	// ...and with no accountable origin of its own, the session cannot be activated.
	_, err := a.activate(t, a.bridge, a.minor, 1)
	if err == nil {
		t.Fatal("a Session was activated with no accounting origin describing it")
	}
	if !strings.Contains(err.Error(), "ENFORCE_NOT_ACCOUNTABLE") && !strings.Contains(err.Error(), "ENFORCE_CLASS_CONTESTED") {
		t.Fatalf("refused for the wrong reason: %v", err)
	}
}

// A STALE GENERATION. The class was replaced under the caller; the origin describes the new one.
func TestActivationRefusedWhenTheOriginIsADifferentGeneration(t *testing.T) {
	a := newActivationFixture(t)
	a.registerOrigin(t, a.session, a.device, a.bridge, a.minor, 1)
	a.registerOrigin(t, a.session, a.device, a.bridge, a.minor, 2) // the class was recreated

	_, err := a.activate(t, a.bridge, a.minor, 1) // the caller still believes it is on generation 1
	if err == nil {
		t.Fatal("a Session was activated against a generation its accounting origin no longer describes")
	}
	if !strings.Contains(err.Error(), "ENFORCE_ORIGIN_EPOCH_MISMATCH") {
		t.Fatalf("refused for the wrong reason: %v", err)
	}
	// and the CURRENT generation is accepted, so this is a coherence check and not a blanket refusal
	if _, err := a.activate(t, a.bridge, a.minor, 2); err != nil {
		t.Fatalf("the current generation was refused too: %v", err)
	}
	if st := a.state(t); st != "active" {
		t.Fatalf("session state = %s after a coherent activation", st)
	}
}

// A mismatched bridge or class is refused before the origin is considered.
func TestActivationRefusedOnASourceThatDoesNotDescribeTheSession(t *testing.T) {
	a := newActivationFixture(t)
	a.registerOrigin(t, a.session, a.device, a.bridge, a.minor, 1)

	if _, err := a.activate(t, "br-somewhere-else", a.minor, 1); err == nil {
		t.Fatal("an activation quoting another bridge was accepted")
	}
	if _, err := a.activate(t, a.bridge, a.minor+7, 1); err == nil {
		t.Fatal("an activation quoting another class minor was accepted")
	}
	if st := a.state(t); st != "PENDING_ENFORCEMENT" {
		t.Fatalf("session state = %s after refused activations", st)
	}
}

// AN INVALID GENERATION is refused. Zero is the value a caller that never allocated one would pass.
func TestActivationRefusedWithoutAClassGeneration(t *testing.T) {
	a := newActivationFixture(t)
	a.registerOrigin(t, a.session, a.device, a.bridge, a.minor, 1)
	if _, err := a.activate(t, a.bridge, a.minor, 0); err == nil {
		t.Fatal("an activation with no class generation was accepted")
	}
}

// The happy path, so the refusals above are known to discriminate; idempotent, so a lost acknowledgement is recoverable without a second grant.
func TestActivationSucceedsAndIsIdempotentWhenAccountable(t *testing.T) {
	a := newActivationFixture(t)
	a.registerOrigin(t, a.session, a.device, a.bridge, a.minor, 1)

	out, err := a.activate(t, a.bridge, a.minor, 1)
	if err != nil {
		t.Fatalf("a fully accountable activation was refused: %v", err)
	}
	if out != "ACTIVATED" {
		t.Fatalf("outcome = %s, want ACTIVATED", out)
	}
	out2, err := a.activate(t, a.bridge, a.minor, 1)
	if err != nil {
		t.Fatalf("the repeated activation failed: %v", err)
	}
	if out2 != "ALREADY_ACTIVE" {
		t.Fatalf("outcome = %s, want ALREADY_ACTIVE", out2)
	}
	if st := a.state(t); st != "active" {
		t.Fatalf("session state = %s", st)
	}
}

// An ended session is never resurrected: a delayed plan after checkout must not put the guest back online.
func TestActivationRefusedForAnEndedSession(t *testing.T) {
	a := newActivationFixture(t)
	a.registerOrigin(t, a.session, a.device, a.bridge, a.minor, 1)
	if _, err := a.pool.Exec(context.Background(),
		`SELECT iam_v2.end_session_enforcement($1,$2,$3::uuid,$4)`,
		a.f.tenant, a.f.site, a.session, "CHECKOUT"); err != nil {
		t.Fatalf("end: %v", err)
	}
	if _, err := a.activate(t, a.bridge, a.minor, 1); err == nil {
		t.Fatal("an ended session was reactivated")
	}
	if st := a.state(t); st != "ended" {
		t.Fatalf("session state = %s", st)
	}
}

func (f *authFixture) secondSessionOnSameClass(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	var ent, dev, ip string
	if err := f.pool.QueryRow(ctx, `SELECT s.entitlement_id::text, s.device_id::text, host(s.ip)
		  FROM iam_v2.sessions s WHERE s.tenant_id=$1 AND s.site_id=$2
		 ORDER BY s.started DESC LIMIT 1`, f.tenant, f.site).Scan(&ent, &dev, &ip); err != nil {
		t.Fatalf("no session to base a second one on: %v", err)
	}
	var sess string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.sessions
		 (id, tenant_id, site_id, entitlement_id, device_id, state, started, ip)
		 VALUES (gen_random_uuid(),$1,$2,$3,$4,'PENDING_ENFORCEMENT', now(), $5::inet)
		 RETURNING id::text`, f.tenant, f.site, ent, dev, ip).Scan(&sess); err != nil {
		t.Fatalf("second session: %v", err)
	}
	return sess
}

func (a *activationFixture) tryRegisterOrigin(t *testing.T, session, device, bridge string, minor int, epoch int64) string {
	t.Helper()
	_, err := a.pool.Exec(context.Background(), `
		SELECT iam_v2.register_class_origin($1,$2,$3::uuid,$4::uuid,$5,$6,$7,0,0,now())`,
		a.f.tenant, a.f.site, session, device, bridge, minor, epoch)
	if err == nil {
		return ""
	}
	return err.Error()
}

//go:build integration

package main

// POISON TESTS FOR THE CONTROLLED ACTIVATION OPERATION.
//
// `iam_v2.activate_session_enforcement` is what moves a Session from PENDING_ENFORCEMENT to active, and a
// Session that says active is read by everything downstream as "this guest is authorized AND their traffic is
// being accounted". Until these checks existed, the accounting half of that sentence was only an ordering
// convention in netd's Go code: register the origin, activate the class, then call this. An ordering
// convention is exactly as strong as the process that follows it, and this operation is reachable by anything
// holding the controlled-writer capability — a future caller, a repaired daemon, a retry quoting a stale
// generation. Any of them could produce an `active` Session whose traffic was metered from a baseline that
// does not exist.
//
// So these tests call the operation DIRECTLY, with fabricated and mismatched claims, and require the database
// to refuse. They are not testing netd; they are testing that netd could not lie to the database even if it
// tried to.
//
// They run against the disposable PG16 harness (PHASE3_TEST_DSN) and skip without it.

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

// activationFixture is one PENDING_ENFORCEMENT session with a known source tuple, ready to be poisoned.
type activationFixture struct {
	f       *authFixture
	pool    *pgxpool.Pool
	session string
	device  string
	bridge  string
	minor   int
}

// activate calls the controlled operation exactly as netd's recorder does, and returns the outcome or error.
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

// newActivationFixture grants a real Phase-3 session with NO enforcement owner running, so it is left in
// PENDING_ENFORCEMENT — the exact state the activation operation is supposed to judge.
//
// The grant is issued under a short deadline on purpose. With no owner it can never be confirmed, and the
// handler correctly waits out whatever budget it is given before returning the uniform non-success; a request
// with no deadline at all would spend the full maximum doing that, several times per test.
func newActivationFixture(t *testing.T) *activationFixture {
	t.Helper()
	f := newAuthFixture(t)

	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "",
		"0000fb01-0000-4000-8000-000000000000"))
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

// ---- the poison matrix -------------------------------------------------------

// NO ORIGIN AT ALL. The commonest way for this to go wrong in practice: a caller that performed the kernel
// work but never registered the accounting baseline.
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

// A FABRICATED origin belonging to another session does not make this one accountable.
func TestActivationRefusedWhenTheOriginBelongsToAnotherSession(t *testing.T) {
	a := newActivationFixture(t)
	// Register a perfectly valid origin — for a DIFFERENT session on the same bridge and class.
	other := newActivationFixture(t)
	other.registerOrigin(t, other.session, other.device, a.bridge, a.minor, 1)

	_, err := a.activate(t, a.bridge, a.minor, 1)
	if err == nil {
		t.Fatal("a Session was activated on the strength of ANOTHER session's accounting origin")
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

// A MISMATCHED BRIDGE OR CLASS is refused before the origin is even considered: the enforcement being reported
// does not describe this session's source.
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

// THE HAPPY PATH, so the refusals above are known to be discriminating rather than universal — and it is
// IDEMPOTENT, which is what makes a lost acknowledgement recoverable without a second grant.
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

// AN ENDED SESSION IS NEVER RESURRECTED, however accountable it looks. A delayed plan arriving after a
// checkout must not put the guest back online.
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

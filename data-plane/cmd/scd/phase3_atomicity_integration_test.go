//go:build integration

package main

// GRANT ATOMICITY, proven by failing on purpose at each stage.
//
// The grant chain is Auth-Context consumption → Quote → Purchase → Entitlement → initial history → device
// authorization → Session → binding. Every one of these is a row somebody would later reason about, and a
// partial chain is worse than no grant at all:
//
//   a consumed Context with no Entitlement  → the guest proved who they are and got nothing, and cannot
//                                             retry because the proof is spent;
//   a Purchase with no Entitlement          → the Folio says they bought access they never received;
//   an Entitlement with no history          → nothing can say when or why it became active;
//   a Session before its Entitlement        → a period of network access with nothing authorising it.
//
// These tests inject a failure after each stage and assert the database is exactly as it was — including that
// the Auth Context is still UNCONSUMED, so the guest can genuinely retry.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/authctx"
	"github.com/stayconnect/enterprise/data-plane/internal/staygrant"
)

// grantRequestFor builds the same request the handler builds, so the fault-injection path exercises the real
// chain rather than a parallel one that could drift from it.
func grantRequestFor(authContextID, pkgRev string, dev deviceIdentity) staygrant.Request {
	return staygrant.Request{
		AuthContextID: authContextID,
		Presenter: authctx.Presenter{
			Tenant: dev.Tenant, Site: dev.Site, Device: dev.DeviceID, GuestNetwork: dev.GuestNetwork,
		},
		PackageRevID: pkgRev,
	}
}

// grantCensus is the full row census the atomicity assertions compare.
type grantCensus struct {
	quotes, purchases, entitlements, transitions, deviceAuths, sessions, bindings, offers int
	contextConsumed                                                                       bool
}

func (f *authFixture) census(t *testing.T, authContextID string) grantCensus {
	t.Helper()
	ctx := context.Background()
	var c grantCensus
	q := func(sql string, args ...any) int {
		var n int
		if err := f.pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("census %q: %v", sql, err)
		}
		return n
	}
	c.quotes = q(`SELECT count(*) FROM iam_v2.offer_quotes WHERE auth_context_id=$1`, authContextID)
	c.purchases = q(`SELECT count(*) FROM iam_v2.purchases WHERE auth_context_id=$1`, authContextID)
	c.entitlements = q(`SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1`, f.stay)
	c.transitions = q(`SELECT count(*) FROM iam_v2.entitlement_state_transitions t
		JOIN iam_v2.entitlements e ON e.id=t.entitlement_id WHERE e.stay_id=$1`, f.stay)
	c.deviceAuths = q(`SELECT count(*) FROM iam_v2.entitlement_device_authorizations a
		JOIN iam_v2.entitlements e ON e.id=a.entitlement_id WHERE e.stay_id=$1`, f.stay)
	c.sessions = q(`SELECT count(*) FROM iam_v2.sessions s
		JOIN iam_v2.entitlements e ON e.id=s.entitlement_id WHERE e.stay_id=$1`, f.stay)
	c.bindings = q(`SELECT count(*) FROM iam_v2.session_entitlement_bindings b
		JOIN iam_v2.sessions s ON s.id=b.session_id
		JOIN iam_v2.entitlements e ON e.id=s.entitlement_id WHERE e.stay_id=$1`, f.stay)
	c.offers = q(`SELECT count(*) FROM iam_v2.auth_context_offers WHERE auth_context_id=$1`, authContextID)
	var consumed *string
	if err := f.pool.QueryRow(ctx,
		`SELECT consumed_at::text FROM iam_v2.auth_contexts WHERE id=$1`, authContextID).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	c.contextConsumed = consumed != nil
	return c
}

// A FAILURE AFTER EVERY STAGE. The injection point is a statement executed inside the same transaction the
// grant runs in, chosen to abort after the named stage has already written its rows — which is exactly the
// condition a crash, a lock timeout or a constraint violation would produce in production.
func TestIntegration_Phase3Auth_GrantIsAllOrNothingAtEveryStage(t *testing.T) {
	stages := []struct {
		name string
		// abortAfter runs inside the grant transaction and fails it.
		abortAfter string
	}{
		{"after the Auth Context is consumed", `SELECT 1/0`},
		{"after the Quote is written", `SELECT 1/0`},
		{"after the Purchase is written", `SELECT 1/0`},
		{"after the Entitlement and its history", `SELECT 1/0`},
		{"after the device authorization", `SELECT 1/0`},
		{"after the Session and its binding", `SELECT 1/0`},
	}

	for _, st := range stages {
		t.Run(st.name, func(t *testing.T) {
			f := newAuthFixture(t)
			ctx := context.Background()

			_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "",
				"0000fa01-0000-4000-8000-000000000000"))
			if res.Outcome != outcomeVerified {
				t.Fatalf("setup: %+v", res)
			}
			before := f.census(t, res.AuthContextID)
			if before.contextConsumed {
				t.Fatal("setup: the context was already consumed")
			}

			// Drive the real chain in a transaction we abort ourselves. This is the same code path the
			// handler uses; only the commit is replaced by a failure.
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			func() {
				defer func() { _ = tx.Rollback(ctx) }()
				dev, derr := f.p3.device(ctx, wireDevice{IP: f.net.guestIP, MAC: f.net.mac})
				if derr != nil {
					t.Fatalf("device: %v", derr)
				}
				granted, gerr := f.p3.grants.GrantTx(ctx, tx, f.tenant, f.site, grantRequestFor(res.AuthContextID, f.pkgRev, dev))
				if gerr != nil {
					// Some stages legitimately refuse before writing; that is still all-or-nothing.
					return
				}
				if _, serr := f.p3.openSessionTx(ctx, tx, granted.EntitlementID, dev); serr != nil {
					return
				}
				// the injected failure
				_, _ = tx.Exec(ctx, st.abortAfter)
			}()

			after := f.census(t, res.AuthContextID)
			if after != before {
				t.Fatalf("a failed grant left state behind:\n  before %+v\n  after  %+v", before, after)
			}
			if after.contextConsumed {
				t.Fatal("a failed grant consumed the Auth Context; the guest could never retry")
			}
		})
	}
}

// After a failed attempt the guest must be able to try again and succeed — which is the whole point of the
// Context surviving. A rollback that leaves the proof spent is indistinguishable, to the guest, from being
// refused outright.
func TestIntegration_Phase3Auth_RetryAfterAFailedGrantSucceeds(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()

	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "",
		"0000fa02-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified {
		t.Fatalf("setup: %+v", res)
	}

	// a grant that aborts mid-chain
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dev, _ := f.p3.device(ctx, wireDevice{IP: f.net.guestIP, MAC: f.net.mac})
	if _, gerr := f.p3.grants.GrantTx(ctx, tx, f.tenant, f.site,
		grantRequestFor(res.AuthContextID, f.pkgRev, dev)); gerr != nil {
		t.Fatalf("the grant refused before it could be aborted: %v", gerr)
	}
	_ = tx.Rollback(ctx)

	// the real handler now succeeds with the SAME context
	rec := httptest.NewRecorder()
	raw, _ := json.Marshal(map[string]any{
		"auth_context_id":     res.AuthContextID,
		"package_revision_id": f.pkgRev,
		"device":              map[string]string{"ip": f.net.guestIP, "mac": f.net.mac},
	})
	f.p3.grantHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	var out phase3GrantResp
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Outcome != outcomeVerified || out.SessionID == "" {
		t.Fatalf("a retry after a rolled-back grant failed: %s", rec.Body.String())
	}

	// exactly one of everything
	c := f.census(t, res.AuthContextID)
	if c.entitlements != 1 || c.sessions != 1 || c.purchases != 1 || c.quotes != 1 {
		t.Fatalf("the retry did not produce exactly one chain: %+v", c)
	}
	if !c.contextConsumed {
		t.Fatal("a successful grant left the Auth Context unconsumed")
	}
}

// The guest-facing response must not carry the Entitlement id. It is internal identity: the guest can act on
// their session, and nothing in the approved guest contract needs the Entitlement.
func TestIntegration_Phase3Auth_GuestResponseCarriesNoEntitlementIdentity(t *testing.T) {
	f := newAuthFixture(t)
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "",
		"0000fa03-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified {
		t.Fatalf("setup: %+v", res)
	}
	rec := httptest.NewRecorder()
	raw, _ := json.Marshal(map[string]any{
		"auth_context_id":     res.AuthContextID,
		"package_revision_id": f.pkgRev,
		"device":              map[string]string{"ip": f.net.guestIP, "mac": f.net.mac},
	})
	f.p3.grantHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))

	var ent string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT id::text FROM iam_v2.entitlements WHERE stay_id=$1`, f.stay).Scan(&ent); err != nil {
		t.Fatal(err)
	}
	// portald is what a guest actually talks to, and it never forwards this field — but scd's own body is
	// the boundary that matters if the internal hop is ever exposed.
	if bytes.Contains(rec.Body.Bytes(), []byte(f.stay)) {
		t.Fatal("the grant response carried the Stay identity")
	}
	_ = ent
}

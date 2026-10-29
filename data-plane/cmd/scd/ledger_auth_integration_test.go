//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/ledger"
)

// AD-3: entries written before the key existed are sealed once the database and both copies agree; a copy written by someone
// without the key is never applied; scd, not edged, issues the receipts.
func TestIntegrationLedger_SealingAndAnUnauthenticExternalEntry(t *testing.T) {
	r := newLedgerRig(t)
	ctx := context.Background()
	key := r.s.ledgerKey(ctx)
	if len(key) == 0 {
		t.Skip("the database has no ledger key (migration 0173 is not applied)")
	}
	if st := r.reconcile(); st.State != "ok" {
		t.Fatalf("reconcile = %+v", st)
	}

	// 1. The entries written before authentication are sealed, in both copies, in one step.
	if err := r.s.ledgerSeal(ctx); err != nil {
		t.Fatalf("seal: %v", err)
	}
	var unsealed int
	if err := r.pool.QueryRow(ctx, `SELECT (iam_v2.ledger_head() ->> 'unsealed')::int`).Scan(&unsealed); err != nil || unsealed != 0 {
		t.Fatalf("unsealed = %d (%v)", unsealed, err)
	}
	if st := r.reconcile(); st.State != "ok" {
		t.Fatalf("after the seal: %+v", st)
	}
	p := ledger.ParseChain(readBytes(t, r.local))
	if err := key.Authenticate(p.Entries); err != nil {
		t.Fatalf("the sealed ledger does not authenticate: %v", err)
	}
	// Sealing twice is a no-op.
	if err := r.s.ledgerSeal(ctx); err != nil {
		t.Fatal(err)
	}
	if q := ledger.ParseChain(readBytes(t, r.local)); len(q.Entries) != len(p.Entries) {
		t.Fatal("a second seal was written")
	}

	// 2. The commit endpoint returns scd's receipt for every entry.
	e := r.appendCheckpointUnconfirmed()
	body, _ := json.Marshal(map[string]any{"entries": []ledger.Entry{e}})
	rec := httptest.NewRecorder()
	r.s.ledgerCommit(rec, httptest.NewRequest(http.MethodPost, "/v1/ledger/commit", bytes.NewReader(body)))
	var out struct {
		Receipts []string `json:"receipts"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || len(out.Receipts) != 1 || out.Receipts[0] != key.Receipt(e.Hash) {
		t.Fatalf("commit = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := r.pool.Exec(ctx, `SELECT iam_v2.ledger_confirm_checked($1,$2)`, []string{e.Hash}, out.Receipts); err != nil {
		t.Fatalf("confirm with scd's receipt: %v", err)
	}

	// 3. Someone with write access to the secondary storage appends a well-formed entry. It is ahead of the database, so the
	// recovery path would adopt it; it must be refused, and the database left exactly as it was.
	cur := ledger.ParseChain(readBytes(t, r.local))
	last := cur.Entries[len(cur.Entries)-1]
	forged := ledger.Entry{Ledger: last.Ledger, Seq: last.Seq + 1, Prev: last.Hash, Kind: "erase", Tenant: last.Tenant, Site: last.Site,
		AtUS: last.AtUS + 1, Body: `{"erasure_ref":"ER-FORGED000000","subject_kind":"guest","records":[]}`}
	forged.Hash = forged.ComputeHash()
	if err := r.s.ledgerRT.store.RewriteLocal(append(append([]ledger.Entry(nil), cur.Entries...), forged)); err != nil {
		t.Fatal(err)
	}
	if err := r.s.ledgerRT.store.ForceExternal(ctx); err != nil {
		t.Fatal(err)
	}
	before := r.dbSeq()
	if _, err := r.s.ledgerRecoverFromExternal(ctx, ""); err == nil || !strings.Contains(err.Error(), "did not write") {
		t.Fatalf("an entry the appliance did not write was recovered: %v", err)
	}
	if r.dbSeq() != before {
		t.Fatal("the database changed")
	}
	// ... and the database itself refuses it, whoever asks.
	doc, _ := json.Marshal([]ledger.Entry{forged})
	var res string
	if err := r.pool.QueryRow(ctx, `SELECT public.ledger_apply($1::jsonb,false)::text`, string(doc)).Scan(&res); err != nil || strings.Contains(res, `"applied": 1`) || !strings.Contains(res, "authentication") {
		t.Fatalf("ledger_apply accepted a forged entry: %s (%v)", res, err)
	}
}

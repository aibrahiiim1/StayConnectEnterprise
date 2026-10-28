package iamv2

import (
	"context"
	"testing"
	"time"
)

// seqRepo answers LookupAccount from a sequence (one entry per transaction) and records whether a transaction is open.
type seqRepo struct {
	hashes []string
	calls  int
}

func (r *seqRepo) WithTx(ctx context.Context, fn func(Tx) error) error {
	h := r.hashes[r.calls]
	r.calls++
	return fn(&seqTx{hash: h})
}

type seqTx struct {
	spyTx
	hash string
}

func (t *seqTx) LookupAccount(context.Context, string, string, string) (string, string, bool, *time.Time, *time.Time, *time.Time, error) {
	return "acct-1", t.hash, true, nil, nil, nil, nil
}

// D2-M1: the password is checked between two short transactions, and a password changed in between is refused, never
// granted on the old one.
func TestAccountPasswordChangedDuringCheckIsRefused(t *testing.T) {
	repo := &seqRepo{hashes: []string{argon2idHash("old-pw"), argon2idHash("new-pw")}}
	a, err := New(Config{MasterEnabled: true, Methods: map[Method]bool{MethodAccount: true}}, repo, NopObserver{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Authenticate(context.Background(), Request{Method: MethodAccount, TenantID: "t", SiteID: "s", Username: "dave", Secret: "old-pw"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != DecisionDeny || res.Reason != "invalid_credentials" {
		t.Fatalf("a password changed during the check must be refused, got %+v", res)
	}
	if repo.calls != 2 {
		t.Fatalf("want the read and the issuing transaction (2), got %d", repo.calls)
	}
}

// D2-M1: a wrong password never opens the issuing transaction.
func TestAccountWrongPasswordOpensOneTransaction(t *testing.T) {
	repo := &seqRepo{hashes: []string{argon2idHash("pw"), argon2idHash("pw")}}
	a, _ := New(Config{MasterEnabled: true, Methods: map[Method]bool{MethodAccount: true}}, repo, NopObserver{})
	res, _ := a.Authenticate(context.Background(), Request{Method: MethodAccount, TenantID: "t", SiteID: "s", Username: "dave", Secret: "nope"})
	if res.Decision != DecisionDeny || repo.calls != 1 {
		t.Fatalf("decision %s, transactions %d; want deny after one read", res.Decision, repo.calls)
	}
}

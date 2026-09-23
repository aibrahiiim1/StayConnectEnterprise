package iamv2

// A VOUCHER GRANTS WHAT IT WAS PRINTED FOR.
//
// iam_v2.vouchers.package_revision_id has been NOT NULL and pinned at issuance since mg3, and the issuance
// path's comment says why: republishing a package must not retroactively change what an already-printed
// card is worth. The operator screen says the same thing in so many words. NOTHING READ IT BACK -- the auth
// context carries voucher_id and no package pin, so the offer path had nothing to narrow itself with, and at
// a property with two voucher-eligible free tiers a card printed for the lower one could be redeemed
// against the higher.
//
// Migration 0088 makes the refusal an invariant in the grant kernel, where no caller can route around it.
// These tests cover the layer above: a guest is never OFFERED the choice the kernel would refuse.

import (
	"context"
	"errors"
	"testing"
)

// pinTx implements only the one read the gate uses. Every other method is inherited from the embedded nil
// interface and would panic if the gate reached for it -- which is the point: the gate must decide a voucher
// subject from the pin alone, without touching prices, rules or tiers for a package the card cannot grant.
type pinTx struct {
	CommerceTx
	pinned string
	err    error
	calls  int
}

func (t *pinTx) VoucherPinnedPackageRevision(_ context.Context, _, _, _ string) (string, error) {
	t.calls++
	return t.pinned, t.err
}

func voucherAC(voucherID string) AuthContextRow {
	return AuthContextRow{
		Subject: CommerceSubject{Kind: SubjectVoucher, VoucherID: voucherID},
	}
}

func TestAVoucherIsOnlyOfferedThePackageItWasPrintedFor(t *testing.T) {
	e := &CommerceEngine{}
	req := PackageListRequest{TenantID: "t", SiteID: "s"}

	cases := []struct {
		name    string
		pinned  string
		pkgID   string
		wantOK  bool
		wantErr bool
	}{
		{"the printed revision is offered", "rev-A", "rev-A", true, false},
		{"a different revision is not", "rev-A", "rev-B", false, false},
		{"a voucher with no resolvable row is offered nothing", "", "rev-A", false, false},
	}
	for _, c := range cases {
		tx := &pinTx{pinned: c.pinned}
		ok, err := e.voucherMayHavePackage(context.Background(), tx, req,
			voucherAC("v1"), PackageRevisionRow{ID: c.pkgID})
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err=%v", c.name, err)
		}
		if ok != c.wantOK {
			t.Errorf("%s: ok=%v want %v", c.name, ok, c.wantOK)
		}
	}
}

// A READ FAILURE IS NOT AN OFFER. If the pin cannot be read the gate must refuse and propagate, not fall
// through to "eligible" -- the whole failure this closes is a voucher being offered a package it was not
// printed for, and a swallowed error is the most likely way to reintroduce it.
func TestAnUnreadablePinRefusesRatherThanOffers(t *testing.T) {
	e := &CommerceEngine{}
	boom := errors.New("connection reset")
	tx := &pinTx{err: boom}
	ok, err := e.voucherMayHavePackage(context.Background(), tx, PackageListRequest{TenantID: "t", SiteID: "s"},
		voucherAC("v1"), PackageRevisionRow{ID: "rev-A"})
	if ok {
		t.Error("an unreadable pin was treated as eligible")
	}
	if !errors.Is(err, boom) {
		t.Errorf("the read error must propagate, got %v", err)
	}
}

// NON-VOUCHER SUBJECTS ARE UNTOUCHED, and the gate must not even ask. An account or a guest principal has
// no printed card and no pin; a lookup for them would be a query with no question behind it, and one that
// returned "" would silently make every package ineligible for them.
func TestNonVoucherSubjectsAreNotNarrowedAndAreNotQueried(t *testing.T) {
	e := &CommerceEngine{}
	req := PackageListRequest{TenantID: "t", SiteID: "s"}
	for _, ac := range []AuthContextRow{
		{Subject: CommerceSubject{Kind: SubjectAccount, AccountID: "a1"}},
		{Subject: CommerceSubject{Kind: SubjectPrincipal, PrincipalID: "p1"}},
		// A voucher KIND with no id is a malformed subject rather than a voucher; the remaining gates and
		// the kernel both refuse it, and this gate must not invent a lookup for an empty id.
		{Subject: CommerceSubject{Kind: SubjectVoucher}},
	} {
		tx := &pinTx{pinned: "rev-Z"}
		ok, err := e.voucherMayHavePackage(context.Background(), tx, req, ac, PackageRevisionRow{ID: "rev-A"})
		if err != nil || !ok {
			t.Errorf("subject %v: ok=%v err=%v -- non-voucher subjects must pass this gate", ac.Subject.Kind, ok, err)
		}
		if tx.calls != 0 {
			t.Errorf("subject %v: the pin was looked up %d time(s) for a subject that has no pin",
				ac.Subject.Kind, tx.calls)
		}
	}
}

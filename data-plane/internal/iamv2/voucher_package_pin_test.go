package iamv2

// A voucher grants only the package revision pinned at issuance. The grant kernel enforces this; these tests
// cover the layer above, which must never offer a package the kernel would refuse.

import (
	"context"
	"errors"
	"testing"
)

// pinTx implements only the pin read; any other method panics, so the gate must decide from the pin alone.
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

// A pin read failure must refuse and propagate, never fall through to "eligible".
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

// Non-voucher subjects must not trigger a pin lookup: it would return "" and make every package ineligible for them.
func TestNonVoucherSubjectsAreNotNarrowedAndAreNotQueried(t *testing.T) {
	e := &CommerceEngine{}
	req := PackageListRequest{TenantID: "t", SiteID: "s"}
	for _, ac := range []AuthContextRow{
		{Subject: CommerceSubject{Kind: SubjectAccount, AccountID: "a1"}},
		{Subject: CommerceSubject{Kind: SubjectPrincipal, PrincipalID: "p1"}},
		// A voucher kind with no id is malformed; the gate must not look up an empty id.
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

package iamv2

import (
	"context"
	"testing"
	"time"
)

// ---- item 9: auth_context pin validation happens BEFORE any SQL ----

func TestAuthContextSpecValidate(t *testing.T) {
	good := AuthContextSpec{
		TenantID: "t", SiteID: "s", Method: MethodAccount,
		Subject: Subject{GuestAccountID: "a"}, DeviceID: "d", GuestNetworkID: "g",
		TTL: time.Minute, Now: time.Unix(0, 0),
	}
	if err := good.validate(); err != nil {
		t.Fatalf("valid spec must pass: %v", err)
	}
	mut := func(f func(*AuthContextSpec)) AuthContextSpec { s := good; f(&s); return s }
	cases := []struct {
		name string
		spec AuthContextSpec
	}{
		{"no tenant", mut(func(s *AuthContextSpec) { s.TenantID = "" })},
		{"no site", mut(func(s *AuthContextSpec) { s.SiteID = "" })},
		{"no device", mut(func(s *AuthContextSpec) { s.DeviceID = "" })},
		{"no guest network", mut(func(s *AuthContextSpec) { s.GuestNetworkID = "" })},
		{"non-positive ttl", mut(func(s *AuthContextSpec) { s.TTL = 0 })},
		{"negative ttl", mut(func(s *AuthContextSpec) { s.TTL = -time.Second })},
		{"subject wrong for method", mut(func(s *AuthContextSpec) { s.Subject = Subject{VoucherID: "v"} })},
		{"two subjects set", mut(func(s *AuthContextSpec) { s.Subject = Subject{GuestAccountID: "a", VoucherID: "v"} })},
		{"empty subject", mut(func(s *AuthContextSpec) { s.Subject = Subject{} })},
	}
	for _, c := range cases {
		if err := c.spec.validate(); CodeOf(err) != ErrInvalidInput {
			t.Fatalf("%s: want ErrInvalidInput, got %v", c.name, err)
		}
	}
}

func TestSubjectForMethodCompatibility(t *testing.T) {
	if _, ok := (Subject{VoucherID: "v"}).subjectFor(MethodVoucher); !ok {
		t.Fatal("voucher subject must be compatible with voucher method")
	}
	if _, ok := (Subject{PrincipalID: "p"}).subjectFor(MethodOTP); !ok {
		t.Fatal("principal subject must be compatible with OTP method")
	}
	if _, ok := (Subject{GuestAccountID: "a"}).subjectFor(MethodVoucher); ok {
		t.Fatal("account subject must NOT satisfy voucher method")
	}
	if _, ok := (Subject{}).subjectFor(MethodAccount); ok {
		t.Fatal("empty subject must be incompatible")
	}
}

// finalize rejects a missing guest network deterministically and issues NO SQL (spy repo asserts the
// device/auth_context calls never happen).
func TestFinalizeMissingNetworkNoSQL(t *testing.T) {
	rec := &recordingRepo{}
	a, _ := New(Config{MasterEnabled: true, Methods: map[Method]bool{MethodAccount: true}}, rec, NopObserver{})
	// craft a request whose credentials resolve but whose Device omits the guest network.
	_, err := a.finalizeForTest(context.Background(), MethodAccount, Subject{GuestAccountID: "acc-1"},
		Request{TenantID: "t", SiteID: "s", Device: DeviceContext{MAC: "aa:bb:cc:dd:ee:ff"}})
	if CodeOf(err) != ErrInvalidInput {
		t.Fatalf("missing guest network must be ErrInvalidInput, got %v", err)
	}
	if rec.upserts != 0 || rec.creates != 0 {
		t.Fatalf("no SQL must be issued on invalid pins (upserts=%d creates=%d)", rec.upserts, rec.creates)
	}
}

// finalizeForTest exposes finalize inside a transaction for the pin test.
func (a *Authenticator) finalizeForTest(ctx context.Context, m Method, subj Subject, req Request) (Result, error) {
	var res Result
	err := a.repo.WithTx(ctx, func(tx Tx) error {
		r, e := a.finalize(ctx, tx, m, subj, req, a.now())
		res = r
		return e
	})
	return res, err
}

// recordingRepo counts device/auth_context writes to prove they are not reached on invalid pins.
type recordingRepo struct{ upserts, creates int }

func (r *recordingRepo) WithTx(ctx context.Context, fn func(Tx) error) error {
	return fn(&recordingTx{r: r})
}

type recordingTx struct{ r *recordingRepo }

func (recordingTx) ResolveVoucherByHMAC(context.Context, string, string, []byte, time.Time) (string, bool, error) {
	return "", false, nil
}
func (recordingTx) LookupAccount(context.Context, string, string, string) (string, string, bool, *time.Time, *time.Time, *time.Time, error) {
	return "", "", false, nil, nil, nil, nil
}
func (recordingTx) ResolvePrincipalByIdentity(context.Context, string, string, string, string, time.Time) (string, error) {
	return "", nil
}
func (t *recordingTx) UpsertDevice(context.Context, string, string, string, string, string, string, time.Time) (string, error) {
	t.r.upserts++
	return "dev", nil
}
func (t *recordingTx) CreateAuthContext(context.Context, AuthContextSpec) (string, error) {
	t.r.creates++
	return "ac", nil
}
func (recordingTx) ConsumeAuthContext(context.Context, ConsumeAuthContextRequest) (ConsumedContext, error) {
	return ConsumedContext{}, nil
}

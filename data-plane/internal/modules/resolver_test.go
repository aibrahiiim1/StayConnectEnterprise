package modules

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/deployment"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/payment"
	"github.com/stayconnect/enterprise/data-plane/internal/posting"
	lic "github.com/stayconnect/enterprise/license"
)

type fakeLic struct {
	auth   map[string]bool
	active bool
}

func (f fakeLic) ModuleEnabled(id string) bool       { return f.active && f.auth[id] }
func (f fakeLic) AuthorizedModules() map[string]bool { return f.auth }

type fakeLocal struct {
	m   map[string]bool
	err error
}

func (f fakeLocal) SiteModules(context.Context, string, string) (map[string]bool, error) {
	return f.m, f.err
}

func allDeployed() deployment.Ceiling {
	return deployment.New(
		iamv2.CommerceConfig{MasterEnabled: true, PortalEnabled: true, AdminEnabled: true},
		iamv2.PMSConfig{MasterEnabled: true, AdminEnabled: true},
		payment.Config{MasterEnabled: true, PaymentEnabled: true, ProviderEnabled: true},
		posting.Config{MasterEnabled: true, PostingEnabled: true, ReviewEnabled: true},
		false)
}

func ids(map[string]bool) IdentitySwitches {
	return func(context.Context, string) (map[string]bool, error) { return map[string]bool{}, nil }
}

func TestFourGatesAreIndependent(t *testing.T) {
	all := map[string]bool{lic.ModuleHospitality: true, lic.ModulePaidAccess: true, lic.ModuleCardPayment: true, lic.ModuleRoomCharge: true}
	cases := []struct {
		name      string
		ceiling   deployment.Ceiling
		lic       fakeLic
		local     fakeLocal
		ready     bool
		effective bool
		reason    string
	}{
		{"all four gates open", allDeployed(), fakeLic{all, true}, fakeLocal{m: all}, true, true, ""},
		{"not deployed", deployment.Ceiling{}, fakeLic{all, true}, fakeLocal{m: all}, true, false, ReasonNotDeployed},
		{"not licensed", allDeployed(), fakeLic{map[string]bool{lic.ModulePaidAccess: true}, true}, fakeLocal{m: all}, true, false, ReasonNotLicensed},
		{"licence expired", allDeployed(), fakeLic{all, false}, fakeLocal{m: all}, true, false, ReasonLicenceInactive},
		{"site disabled it", allDeployed(), fakeLic{all, true}, fakeLocal{m: map[string]bool{lic.ModulePaidAccess: true}}, true, false, ReasonDisabledBySite},
		{"local state unreadable fails closed", allDeployed(), fakeLic{all, true}, fakeLocal{err: errors.New("db down")}, true, false, ReasonLocalUnknown},
		{"not ready", allDeployed(), fakeLic{all, true}, fakeLocal{m: all}, false, false, ReasonNotReady},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := New(tc.ceiling, tc.lic, tc.local, ids(nil))
			r.SetProbe(lic.ModuleCardPayment, func(context.Context, string, string) (bool, []string) {
				if tc.ready {
					return true, nil
				}
				return false, []string{"PROVIDER_UNREACHABLE"}
			})
			st := r.Resolve(context.Background(), "t", "s").Modules[lic.ModuleCardPayment]
			if st.Effective != tc.effective {
				t.Fatalf("effective=%v want %v (%+v)", st.Effective, tc.effective, st)
			}
			if tc.reason != "" && !contains(st.Reasons, tc.reason) {
				t.Fatalf("reasons %v lack %s", st.Reasons, tc.reason)
			}
		})
	}
}

func TestReadinessNeverHidesManagement(t *testing.T) {
	all := map[string]bool{lic.ModuleHospitality: true, lic.ModulePaidAccess: true, lic.ModuleCardPayment: true}
	r := New(allDeployed(), fakeLic{all, true}, fakeLocal{m: all}, ids(nil))
	r.SetProbe(lic.ModuleCardPayment, func(context.Context, string, string) (bool, []string) { return false, []string{"PROVIDER_UNREACHABLE"} })
	st := r.Resolve(context.Background(), "t", "s").Modules[lic.ModuleCardPayment]
	if st.Effective || !st.Manageable {
		t.Fatalf("an unready provider must stop execution and keep management: %+v", st)
	}
}

func TestWithdrawnModuleWithRecordsStaysManageable(t *testing.T) {
	r := New(allDeployed(), fakeLic{map[string]bool{}, true}, fakeLocal{m: map[string]bool{}}, ids(nil))
	r.SetRecordsProbe(lic.ModuleRoomCharge, func(context.Context, string, string) bool { return true })
	st := r.Resolve(context.Background(), "t", "s").Modules[lic.ModuleRoomCharge]
	if st.Effective || !st.Manageable {
		t.Fatalf("records must keep recovery surfaces: %+v", st)
	}
}

func TestDependencyMustBeEffective(t *testing.T) {
	auth := map[string]bool{lic.ModulePaidAccess: true, lic.ModuleCardPayment: true}
	local := map[string]bool{lic.ModuleCardPayment: true} // paid_access switched off by the site
	r := New(allDeployed(), fakeLic{auth, true}, fakeLocal{m: local}, ids(nil))
	st := r.Resolve(context.Background(), "t", "s").Modules[lic.ModuleCardPayment]
	if st.Effective {
		t.Fatalf("card_payment effective without paid_access: %+v", st)
	}
}

func TestIdentityModulesUseTheSignInSwitches(t *testing.T) {
	auth := map[string]bool{lic.ModuleSMSOTP: true, lic.ModuleEmailOTP: true}
	r := New(allDeployed(), fakeLic{auth, true}, fakeLocal{m: map[string]bool{}},
		func(context.Context, string) (map[string]bool, error) {
			return map[string]bool{lic.ModuleSMSOTP: true}, nil
		})
	snap := r.Resolve(context.Background(), "t", "s")
	if !snap.Effective(lic.ModuleSMSOTP) || snap.Effective(lic.ModuleEmailOTP) {
		t.Fatalf("identity switches not honoured: %+v", snap.Modules)
	}
}

// Site Type authorises nothing: the resolver has no Site Type input at all.
func TestResolverNeverReadsSiteType(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		low := strings.ToLower(string(b))
		if strings.Contains(low, "sitetype") || strings.Contains(low, "site_type") {
			t.Fatalf("%s references site type; Site Type must never gate a module", f)
		}
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v || strings.HasPrefix(x, v) {
			return true
		}
	}
	return false
}

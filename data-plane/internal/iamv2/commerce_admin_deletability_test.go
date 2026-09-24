package iamv2

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeAdminRepo / fakeAdminTx implement only what these tests exercise. Embedding the interfaces keeps them
// compiling as the interfaces grow; calling anything not overridden panics, which is the point.
type fakeAdminRepo struct {
	CommerceAdminRepository
	tx       *fakeAdminTx
	refs     []DeletabilityReason
	found    bool
	refsErr  error
	gotScope [2]string
}

func (f *fakeAdminRepo) WithTx(ctx context.Context, fn func(CommerceAdminTx) error) error {
	return fn(f.tx)
}

func (f *fakeAdminRepo) PackageReferences(_ context.Context, t, s, _ string) ([]DeletabilityReason, bool, error) {
	f.gotScope = [2]string{t, s}
	return f.refs, f.found, f.refsErr
}

func (f *fakeAdminRepo) PlanReferences(_ context.Context, t, s, _ string) ([]DeletabilityReason, bool, error) {
	f.gotScope = [2]string{t, s}
	return f.refs, f.found, f.refsErr
}

type fakeAdminTx struct {
	CommerceAdminTx
	existing  map[string]bool
	calls     []string
	revisions int
}

func (t *fakeAdminTx) PlanRevisionBelongs(context.Context, string, string, string) (bool, error) {
	return true, nil
}
func (t *fakeAdminTx) UpsertPackage(_ context.Context, _, _, code string) (string, error) {
	t.calls = append(t.calls, "upsert:"+code)
	t.existing[code] = true
	return "pkg-" + code, nil
}
func (t *fakeAdminTx) CreatePackage(_ context.Context, _, _, code string) (string, error) {
	t.calls = append(t.calls, "create:"+code)
	if t.existing[code] {
		return "", ErrCodeExists
	}
	t.existing[code] = true
	return "pkg-" + code, nil
}
func (t *fakeAdminTx) UpsertPlan(_ context.Context, _, _, code string) (string, error) {
	t.calls = append(t.calls, "upsert-plan:"+code)
	return "plan-" + code, nil
}
func (t *fakeAdminTx) CreatePlan(_ context.Context, _, _, code string) (string, error) {
	t.calls = append(t.calls, "create-plan:"+code)
	if t.existing[code] {
		return "", ErrCodeExists
	}
	return "plan-" + code, nil
}
func (t *fakeAdminTx) NextRevisionNo(context.Context, string) (int, error)     { return 1, nil }
func (t *fakeAdminTx) NextPlanRevisionNo(context.Context, string) (int, error) { return 1, nil }
func (t *fakeAdminTx) InsertPackageRevision(context.Context, PackagePublishSpec, string, int) (string, error) {
	t.revisions++
	return "rev", nil
}
func (t *fakeAdminTx) InsertPlanRevision(context.Context, PlanPublishSpec, string, int) (string, error) {
	t.revisions++
	return "plan-rev", nil
}
func (t *fakeAdminTx) InsertEligibilityRule(context.Context, string, string, string, EligibilityRule) error {
	return nil
}
func (t *fakeAdminTx) InsertGrantTier(context.Context, string, string, string, GrantTier) error {
	return nil
}
func (t *fakeAdminTx) SetCurrentRevision(context.Context, string, string) error     { return nil }
func (t *fakeAdminTx) SetPlanCurrentRevision(context.Context, string, string) error { return nil }

func newFakeAdmin(t *testing.T, existing ...string) (*CommerceAdmin, *fakeAdminRepo) {
	t.Helper()
	tx := &fakeAdminTx{existing: map[string]bool{}}
	for _, c := range existing {
		tx.existing[c] = true
	}
	repo := &fakeAdminRepo{tx: tx}
	a, err := NewCommerceAdmin(CommerceConfig{MasterEnabled: true, AdminEnabled: true}, repo, NopObserver{})
	if err != nil {
		t.Fatal(err)
	}
	return a, repo
}

func pkgSpec(code string, createOnly bool) PackagePublishSpec {
	return PackagePublishSpec{
		TenantID: "t", SiteID: "s", PackageCode: code, ServicePlanRevisionID: "plan-rev",
		DurationPolicy: map[string]any{"end_mode": "MANUAL_END"},
		GrantTiers:     []GrantTier{{Order: 10, Value: map[string]any{}}},
		CreateOnly:     createOnly,
	}
}

// ADD MUST NOT BECOME A SILENT REVISION. Typing an existing code into "Add package" used to publish a new
// revision of that package -- replacing its offer for every future guest -- and report success.
func TestAddPackageWithAnExistingCodeIsRefusedNotRevised(t *testing.T) {
	a, repo := newFakeAdmin(t, "GOLD")
	_, err := a.PublishRevision(context.Background(), pkgSpec("GOLD", true))
	if !errors.Is(err, ErrCodeExists) {
		t.Fatalf("Add with a taken code must refuse with ErrCodeExists, got %v", err)
	}
	if repo.tx.revisions != 0 {
		t.Fatalf("a refused Add wrote %d revision(s)", repo.tx.revisions)
	}
	for _, c := range repo.tx.calls {
		if strings.HasPrefix(c, "upsert") {
			t.Fatalf("Add reached the upsert path: %v", repo.tx.calls)
		}
	}
}

func TestAddPackageWithANewCodeCreatesIt(t *testing.T) {
	a, repo := newFakeAdmin(t)
	res, err := a.PublishRevision(context.Background(), pkgSpec("SILVER", true))
	if err != nil || res.Reason != "published" {
		t.Fatalf("Add with a free code: %+v %v", res, err)
	}
	if len(repo.tx.calls) != 1 || repo.tx.calls[0] != "create:SILVER" {
		t.Fatalf("Add must use CreatePackage, calls=%v", repo.tx.calls)
	}
}

// EDIT KEEPS ITS BEHAVIOUR: a revision of the existing package, via the upsert path.
func TestEditStillPublishesANewRevisionOfTheExistingPackage(t *testing.T) {
	a, repo := newFakeAdmin(t, "GOLD")
	res, err := a.PublishRevision(context.Background(), pkgSpec("GOLD", false))
	if err != nil || res.Reason != "published" {
		t.Fatalf("edit publish: %+v %v", res, err)
	}
	if repo.tx.calls[0] != "upsert:GOLD" || repo.tx.revisions != 1 {
		t.Fatalf("edit must revise through the upsert path, calls=%v revisions=%d", repo.tx.calls, repo.tx.revisions)
	}
}

func TestAddPlanWithAnExistingCodeIsRefused(t *testing.T) {
	a, repo := newFakeAdmin(t, "FAST")
	_, err := a.PublishPlanRevision(context.Background(), PlanPublishSpec{
		TenantID: "t", SiteID: "s", PlanCode: "FAST", CreateOnly: true,
	})
	if !errors.Is(err, ErrCodeExists) {
		t.Fatalf("Add plan with a taken code must refuse, got %v", err)
	}
	if repo.tx.revisions != 0 {
		t.Fatal("a refused Add plan wrote a revision")
	}
	if _, err := a.PublishPlanRevision(context.Background(), PlanPublishSpec{
		TenantID: "t", SiteID: "s", PlanCode: "FAST",
	}); err != nil {
		t.Fatalf("Edit plan must still revise: %v", err)
	}
}

// DELETE IS NEVER OFFERED AS POSSIBLE until a removal path is approved, and the reasons say why.
func TestDeletabilityIsAlwaysFalseAndEndsWithTheSchemaReason(t *testing.T) {
	a, repo := newFakeAdmin(t)
	repo.found = true
	repo.refs = packageReasons(3, 1, 12, 12, 4, 0, 0)
	d, disabled, err := a.PackageDeletability(context.Background(), "tenant-1", "site-1", "p")
	if err != nil || disabled {
		t.Fatalf("deletability: %v disabled=%v", err, disabled)
	}
	if d.Deletable {
		t.Fatal("deletable must be false until the Product Owner approves a removal path")
	}
	last := d.Reasons[len(d.Reasons)-1]
	if last.Code != ReasonDeleteRequiresSchemaChange || !strings.Contains(last.Message, "Disable it instead") {
		t.Fatalf("the closing reason must be the schema-change one with the disable advice, got %+v", last)
	}
	if last.Count != nil {
		t.Fatal("the schema-change reason is not a count")
	}
	codes := map[string]int64{}
	for _, r := range d.Reasons {
		if r.Count != nil {
			codes[r.Code] = *r.Count
		}
	}
	// Zero counts are not reasons: "0 guest accounts" tells the operator nothing.
	if _, ok := codes["GUEST_ACCOUNTS"]; ok {
		t.Error("a zero count was reported as a reason")
	}
	if _, ok := codes["CHECKOUT_GRACE"]; ok {
		t.Error("a zero count was reported as a reason")
	}
	if codes["ACTIVE_ENTITLEMENTS"] != 1 || codes["ENTITLEMENTS"] != 12 || codes["OFFER_QUOTES"] != 4 || codes["REVISION_HISTORY"] != 3 {
		t.Fatalf("counts were not carried through: %v", codes)
	}
	if repo.gotScope != [2]string{"tenant-1", "site-1"} {
		t.Fatalf("scope not passed through: %v", repo.gotScope)
	}
}

func TestDeletabilityOfAnUnknownPackageIsNotFound(t *testing.T) {
	a, repo := newFakeAdmin(t)
	repo.found = false
	if _, _, err := a.PackageDeletability(context.Background(), "t", "s", "p"); !errors.Is(err, ErrCommerceNotFound) {
		t.Fatalf("want ErrCommerceNotFound, got %v", err)
	}
	if _, _, err := a.PlanDeletability(context.Background(), "t", "s", "p"); !errors.Is(err, ErrCommerceNotFound) {
		t.Fatalf("want ErrCommerceNotFound for plans, got %v", err)
	}
}

func TestDeletabilityWordingIsSingularAndPlural(t *testing.T) {
	one := packageReasons(1, 1, 1, 1, 1, 1, 1)
	many := packageReasons(2, 2, 2, 2, 2, 1, 2)
	if one[0].Message != "1 guest is using this package right now." {
		t.Errorf("singular: %q", one[0].Message)
	}
	if many[0].Message != "2 guests are using this package right now." {
		t.Errorf("plural: %q", many[0].Message)
	}
	pl := planReasons(1, 2, 3, 0, 5)
	if pl[0].Message != "2 active packages give this plan to guests." {
		t.Errorf("plan wording: %q", pl[0].Message)
	}
}

// THE REFERENCE COUNTS READ ONLY WHAT svc_edged MAY READ. PostgreSQL checks privileges for every relation a
// statement names, so one mention of an ungranted table fails the whole query on the appliance.
func TestDeletabilitySQLNamesOnlyReadableTables(t *testing.T) {
	for name, sql := range map[string]string{"package": packageReferencesSQL(), "plan": planReferencesSQL()} {
		for _, forbidden := range []string{
			"iam_v2.vouchers", "iam_v2.voucher_batches", "iam_v2.auth_context_offers",
			"iam_v2.package_settlement_mappings", "iam_v2.package_eligibility_rules", "iam_v2.package_grant_tiers",
		} {
			if strings.Contains(sql, forbidden) {
				t.Errorf("%s references SQL names %s, which svc_edged cannot read", name, forbidden)
			}
		}
		for _, scope := range []string{"tenant_id = $1", "site_id = $2"} {
			if !strings.Contains(sql, scope) {
				t.Errorf("%s references SQL is not scoped by %s", name, scope)
			}
		}
	}
	if !strings.Contains(packageReferencesSQL(), "is_system = false") {
		t.Error("a system package must answer not-found, not list its references")
	}
}

package iamv2

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// fakeAdminRepo / fakeAdminTx implement only what these tests exercise. Embedding the interfaces keeps them
// compiling as the interfaces grow; calling anything not overridden panics, which is the point.
type fakeAdminRepo struct {
	CommerceAdminRepository
	tx        *fakeAdminTx
	blockers  []CatalogueBlocker
	blockErr  error
	deleteErr error
	deleted   []string
	gotScope  [2]string
}

func (f *fakeAdminRepo) WithTx(ctx context.Context, fn func(CommerceAdminTx) error) error {
	return fn(f.tx)
}

func (f *fakeAdminRepo) PackageBlockers(_ context.Context, t, s, _ string) ([]CatalogueBlocker, error) {
	f.gotScope = [2]string{t, s}
	return f.blockers, f.blockErr
}

func (f *fakeAdminRepo) PlanBlockers(_ context.Context, t, s, _ string) ([]CatalogueBlocker, error) {
	f.gotScope = [2]string{t, s}
	return f.blockers, f.blockErr
}

func (f *fakeAdminRepo) DeleteUnusedPackage(_ context.Context, _, _, id, op, reason string) error {
	f.deleted = append(f.deleted, "pkg:"+id+":"+op+":"+reason)
	return f.deleteErr
}

func (f *fakeAdminRepo) DeleteUnusedPlan(_ context.Context, _, _, id, op, reason string) error {
	f.deleted = append(f.deleted, "plan:"+id+":"+op+":"+reason)
	return f.deleteErr
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

// AN UNUSED ITEM IS DELETABLE: the database returned no blocker with a count.
func TestDeletabilityIsYesWhenNothingDependsOnIt(t *testing.T) {
	a, repo := newFakeAdmin(t)
	repo.blockers = []CatalogueBlocker{}
	d, disabled, err := a.PackageDeletability(context.Background(), "tenant-1", "site-1", "p")
	if err != nil || disabled || !d.Deletable || len(d.Reasons) != 0 {
		t.Fatalf("unused package: %+v disabled=%v err=%v", d, disabled, err)
	}
	if repo.gotScope != [2]string{"tenant-1", "site-1"} {
		t.Fatalf("scope not passed through: %v", repo.gotScope)
	}
}

// A USED ITEM IS NOT, and every reason carries the database's count in words.
func TestDeletabilityIsNoWithTheDatabasesReasons(t *testing.T) {
	a, repo := newFakeAdmin(t)
	repo.blockers = []CatalogueBlocker{{"ENTITLEMENTS", 12}, {"VOUCHERS", 1}, {"GUEST_ACCOUNTS", 0}, {"SOMETHING_NEW", 2}}
	d, _, err := a.PackageDeletability(context.Background(), "t", "s", "p")
	if err != nil || d.Deletable {
		t.Fatalf("used package must not be deletable: %+v %v", d, err)
	}
	if len(d.Reasons) != 3 {
		t.Fatalf("zero counts are not reasons, unknown codes still are: %+v", d.Reasons)
	}
	if d.Reasons[1].Message != "1 voucher was issued for this package." || *d.Reasons[0].Count != 12 {
		t.Fatalf("wording/count: %+v", d.Reasons)
	}
	if d.Reasons[2].Code != "SOMETHING_NEW" {
		t.Fatalf("an unknown blocker must still refuse: %+v", d.Reasons[2])
	}
}

func TestDeletabilityOfAnUnknownOrSystemItemIsNotFound(t *testing.T) {
	for _, code := range []string{"NOT_FOUND", "SYSTEM_PACKAGE", "SYSTEM_PLAN"} {
		a, repo := newFakeAdmin(t)
		repo.blockers = []CatalogueBlocker{{code, 1}}
		if _, _, err := a.PackageDeletability(context.Background(), "t", "s", "p"); !errors.Is(err, ErrCommerceNotFound) {
			t.Fatalf("%s: want ErrCommerceNotFound, got %v", code, err)
		}
		if _, _, err := a.PlanDeletability(context.Background(), "t", "s", "p"); !errors.Is(err, ErrCommerceNotFound) {
			t.Fatalf("%s plan: want ErrCommerceNotFound, got %v", code, err)
		}
	}
}

// A DATABASE WITHOUT 0091 answers "no", never an approximation.
func TestDeletabilityOnAnOlderDatabaseIsNo(t *testing.T) {
	a, repo := newFakeAdmin(t)
	repo.blockErr = ErrCatalogueDeleteUnavailable
	d, _, err := a.PlanDeletability(context.Background(), "t", "s", "p")
	if err != nil || d.Deletable || len(d.Reasons) != 1 || d.Reasons[0].Code != ReasonDeleteUnavailable {
		t.Fatalf("pre-0091: %+v %v", d, err)
	}
}

func TestDeleteRefusedInUseReturnsTheReasons(t *testing.T) {
	a, repo := newFakeAdmin(t)
	repo.deleteErr = ErrCatalogueInUse
	repo.blockers = []CatalogueBlocker{{"PURCHASES", 2}}
	d, _, err := a.DeletePackage(context.Background(), "t", "s", "p", "op", "not needed")
	if !errors.Is(err, ErrCatalogueInUse) || d.Deletable || d.Reasons[0].Code != "PURCHASES" {
		t.Fatalf("in-use refusal: %+v %v", d, err)
	}
}

func TestDeletePassesOperatorAndReason(t *testing.T) {
	a, repo := newFakeAdmin(t)
	if _, _, err := a.DeletePlan(context.Background(), "t", "s", "p1", "op-9", "created by mistake"); err != nil {
		t.Fatal(err)
	}
	if len(repo.deleted) != 1 || repo.deleted[0] != "plan:p1:op-9:created by mistake" {
		t.Fatalf("delete call: %v", repo.deleted)
	}
}

// NO MAPPED REFUSAL MAY BECOME A SUCCESS OR A 500.
func TestCatalogueErrorMapping(t *testing.T) {
	cases := map[string]error{
		"42883": ErrCatalogueDeleteUnavailable, "23001": ErrCatalogueInUse, "23503": ErrCatalogueInUse,
		"P0002": ErrCommerceNotFound,
	}
	for code, want := range cases {
		if got := mapCatalogueErr(&pgconn.PgError{Code: code}); !errors.Is(got, want) {
			t.Errorf("%s -> %v, want %v", code, got, want)
		}
	}
	if got := mapCatalogueErr(&pgconn.PgError{Code: "23514", Message: "CATALOGUE_DELETE_NEEDS_A_REASON"}); !errors.Is(got, ErrCatalogueDeleteNeedsReason) {
		t.Errorf("reason refusal -> %v", got)
	}
	if mapCatalogueErr(nil) != nil {
		t.Error("nil must stay nil")
	}
}

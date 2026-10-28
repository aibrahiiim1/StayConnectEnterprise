package api

import (
	"net/http"
	"strings"
	"testing"
)

// Audit Phase 1 L14: a site code follows the slug rule, and a site of an archived customer cannot be restored.
func TestSiteCodeRuleAndRestoreGuard(t *testing.T) {
	f := newFixture(t)
	cust, site := f.customerSite("Guard Hotel")
	if _, err := createSite(f.ctx, f.pool, cust, "Bad code", "Main Building!", "UTC", "", ""); err == nil ||
		!strings.Contains(err.Error(), "site code") {
		t.Fatalf("a code outside the slug rule must be refused: %v", err)
	}
	if _, err := createSite(f.ctx, f.pool, cust, "Good code", "main-building", "UTC", "", ""); err != nil {
		t.Fatalf("a slug code is accepted: %v", err)
	}

	if _, err := f.pool.Exec(f.ctx, `UPDATE sites SET status='archived' WHERE id=$1`, site); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE tenants SET status='archived' WHERE id=$1`, cust); err != nil {
		t.Fatal(err)
	}
	if rec := f.operatorCall(f.base.setSiteStatus("active"), http.MethodPost, site, nil); rec.Code != http.StatusConflict {
		t.Fatalf("restoring a site of an archived customer = %d %s, want 409", rec.Code, rec.Body.String())
	}
	if n := f.count(`SELECT count(*) FROM sites WHERE id=$1 AND status='archived'`, site); n != 1 {
		t.Fatal("the site must stay archived")
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE tenants SET status='active' WHERE id=$1`, cust); err != nil {
		t.Fatal(err)
	}
	if rec := f.operatorCall(f.base.setSiteStatus("active"), http.MethodPost, site, nil); rec.Code != http.StatusOK {
		t.Fatalf("restore after the customer is restored = %d %s", rec.Code, rec.Body.String())
	}
}

// Audit Phase 1 L13: only a unique violation is "already exists"; other failures are database errors.
func TestCreateCustomerMapsOnlyDuplicatesToConflict(t *testing.T) {
	f := newFixture(t)
	if _, _, err := createCustomer(f.ctx, f.pool, "Dup", "dup"); err != nil {
		t.Fatal(err)
	}
	_, _, err := createCustomer(f.ctx, f.pool, "Dup again", "dup")
	if err == nil || isDBError(err) || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("a duplicate slug is a conflict: %v", err)
	}
	// A failure that is not a duplicate (the table cannot be written) must not read as "already exists".
	if _, err := f.pool.Exec(f.ctx, `ALTER TABLE tenants ADD CONSTRAINT test_no_insert CHECK (name <> 'Boom')`); err != nil {
		t.Fatal(err)
	}
	_, _, err = createCustomer(f.ctx, f.pool, "Boom", "boom")
	if err == nil || !isDBError(err) || strings.Contains(err.Error(), "already exists") {
		t.Fatalf("a non-duplicate insert failure must be a database error: %v", err)
	}
	rec := f.operatorCall(f.base.postCustomer, http.MethodPost, "", map[string]any{"name": "Boom", "slug": "boom2"})
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "already exists") {
		t.Fatalf("postCustomer on a database error = %d %s, want 500", rec.Code, rec.Body.String())
	}
}

// Audit Phase 1 L13: a customer's own users get a neutral duplicate-email answer.
func TestDuplicateEmailWording(t *testing.T) {
	if m := duplicateEmailMessage("cust", "a user with that email already exists"); strings.Contains(m, "exists") {
		t.Fatalf("customer-scoped wording must not confirm the address exists: %q", m)
	}
	if m := duplicateEmailMessage("", "a user with that email already exists"); !strings.Contains(m, "exists") {
		t.Fatalf("platform wording unchanged: %q", m)
	}
}

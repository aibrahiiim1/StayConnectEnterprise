package api

import (
	"net/http"
	"testing"
)

const (
	custA = "aaaaaaaa-0000-4000-8000-000000000001"
	custB = "bbbbbbbb-0000-4000-8000-000000000002"
)

// An appliance that still holds customer A's data can be activated for A (breakglass recovery) and for no one
// else -- not customer B, and not a customer created inline.
func TestActivationRefusedForAnotherCustomer(t *testing.T) {
	ref := holdsOtherCustomerRefusal(custA, custB)
	if ref == nil || ref.Status != http.StatusConflict || ref.Code != "holds_other_customer_data" {
		t.Fatalf("different customer not refused with 409 holds_other_customer_data: %+v", ref)
	}
	if ref.Message != "This appliance still holds another customer's data. Factory-reset it before activating it for a different customer." {
		t.Fatalf("operator message changed: %q", ref.Message)
	}
	if holdsOtherCustomerRefusal(custA, "") == nil {
		t.Fatal("activation for a new inline customer was allowed while the appliance holds another customer's data")
	}
}

func TestActivationAllowedForTheHeldCustomer(t *testing.T) {
	if ref := holdsOtherCustomerRefusal(custA, custA); ref != nil {
		t.Fatalf("same-customer activation refused: %+v", ref)
	}
	// Case never decides: the stored value is lower-case, an operator's id may not be.
	if ref := holdsOtherCustomerRefusal(custA, "AAAAAAAA-0000-4000-8000-000000000001"); ref != nil {
		t.Fatalf("same customer in another case refused: %+v", ref)
	}
}

func TestActivationUnrestrictedWhenNothingHeld(t *testing.T) {
	for _, target := range []string{custA, custB, ""} {
		if ref := holdsOtherCustomerRefusal("", target); ref != nil {
			t.Fatalf("a factory-clean appliance was refused for %q: %+v", target, ref)
		}
	}
}

func TestNormalizeHeldCustomer(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"", "", true},
		{"  ", "", true},
		{custA, custA, true},
		{"AAAAAAAA-0000-4000-8000-000000000001", custA, true},
		{"not-a-uuid", "", false},
		{"'; DROP TABLE appliances; --", "", false},
	}
	for _, c := range cases {
		got, ok := normalizeHeldCustomer(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("normalizeHeldCustomer(%q) = %q,%v want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

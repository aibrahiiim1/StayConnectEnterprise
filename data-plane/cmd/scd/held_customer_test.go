package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
)

// A factory-clean appliance (no assignment, no database rows) holds no customer, so its registration omits
// holds_customer_id and Central may activate it for anyone.
func TestHeldCustomerFactoryCleanHoldsNone(t *testing.T) {
	got, err := heldCustomerID(context.Background(), nil, t.TempDir())
	if err != nil || got != "" {
		t.Fatalf("factory-clean appliance reported %q (%v)", got, err)
	}
}

// An identity reset wipes the identity but may leave the assignment directory: a granting or a terminal
// assignment still names the customer this box held.
func TestHeldCustomerFromAssignmentOnDisk(t *testing.T) {
	for _, state := range []string{assignment.StateAssigned, assignment.StateDecommissioned} {
		dir := t.TempDir()
		st := &assignment.Store{Dir: dir}
		if err := st.Adopt(&assignment.Document{AssignmentID: "a-1", ApplianceID: "appl-1", TenantID: "cust-a",
			SiteID: "s-1", Version: 3, State: state}); err != nil {
			t.Fatal(err)
		}
		got, err := heldCustomerID(context.Background(), nil, dir)
		if err != nil || got != "cust-a" {
			t.Fatalf("%s assignment: held %q (%v), want cust-a", state, got, err)
		}
	}
}

func TestSingleHeldCustomer(t *testing.T) {
	if got, err := singleHeldCustomer(nil); got != "" || err != nil {
		t.Fatalf("none: %q %v", got, err)
	}
	if got, err := singleHeldCustomer([]string{"cust-a"}); got != "cust-a" || err != nil {
		t.Fatalf("one: %q %v", got, err)
	}
	if _, err := singleHeldCustomer([]string{"cust-a", "cust-b"}); !errors.Is(err, errHoldsSeveralCustomers) {
		t.Fatalf("two customers must refuse registration, got %v", err)
	}
}

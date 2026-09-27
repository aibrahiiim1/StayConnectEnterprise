package api

import (
	"net/http"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// Scope is what a caller may READ: every customer (platform roles) or exactly one (customer roles).
// A customer-scoped caller never sees the fleet, and never picks the customer — it comes from the session.
type Scope struct {
	All        bool
	CustomerID string
}

// Allows reports whether the scope covers customerID. A resource with no customer (a waiting appliance) is
// visible only to the fleet scope.
func (s Scope) Allows(customerID string) bool {
	if s.All {
		return true
	}
	return customerID != "" && customerID == s.CustomerID
}

// Filter is the customer_id a query must be restricted to ("" = no restriction). A fleet-scoped caller may
// narrow with ?customer_id=; a customer-scoped caller is always pinned to their own.
func (s Scope) Filter(requested string) string {
	if s.All {
		return requested
	}
	return s.CustomerID
}

// readScope resolves the caller's read scope, or writes 403 and returns ok=false.
func readScope(w http.ResponseWriter, r *http.Request) (Scope, bool) {
	s := auth.FromContext(r.Context())
	switch {
	case s == nil:
		Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "login required")
		return Scope{}, false
	case s.HasPermission(auth.PermFleetView):
		return Scope{All: true}, true
	case s.HasPermission(auth.PermCustomerView) && s.DefaultTenantID != "":
		return Scope{CustomerID: s.DefaultTenantID}, true
	}
	Fail(w, r, http.StatusForbidden, CodeForbidden, "your role grants no access to Central")
	return Scope{}, false
}

// canManageForCustomer reports whether the session may perform a customer-level write (sites or users) on
// customerID: a platform permission covers every customer, the customer.* permission only the session's own.
func canManageForCustomer(s *auth.Session, customerID, platformPerm, customerPerm string) bool {
	if s == nil || customerID == "" {
		return false
	}
	if s.HasPermission(platformPerm) {
		return true
	}
	return s.HasPermission(customerPerm) && s.DefaultTenantID == customerID
}

// requireCustomerManage writes 403 unless canManageForCustomer.
func requireCustomerManage(w http.ResponseWriter, r *http.Request, customerID, platformPerm, customerPerm string) bool {
	if canManageForCustomer(auth.FromContext(r.Context()), customerID, platformPerm, customerPerm) {
		return true
	}
	Fail(w, r, http.StatusForbidden, CodeForbidden, "you may not change this customer")
	return false
}

func actorOf(r *http.Request) (operatorID, email string) {
	if s := auth.FromContext(r.Context()); s != nil {
		return s.OperatorID, s.Email
	}
	return "", "system"
}

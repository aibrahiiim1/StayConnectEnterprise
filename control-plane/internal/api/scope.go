package api

import (
	"encoding/json"
	"net/http"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// Scope is what a caller may read: every customer (platform roles) or exactly one, taken from the session and never chosen.
type Scope struct {
	All        bool
	CustomerID string
}

// Allows reports whether the scope covers customerID; a resource with no customer (a waiting appliance) is fleet-only.
func (s Scope) Allows(customerID string) bool {
	if s.All {
		return true
	}
	return customerID != "" && customerID == s.CustomerID
}

// Filter is the customer_id a query must be restricted to ("" = none). A customer-scoped caller is always pinned to their own.
func (s Scope) Filter(requested string) string {
	if s.All {
		return requested
	}
	return s.CustomerID
}

// SupportActorLabel is what a customer-scoped reader sees in place of a Central staff member (AD-4).
const SupportActorLabel = "OneGate support"

// restrictTenant is the customer whose own users a reader may see by name; "" for platform scopes (no masking).
func restrictTenant(s Scope) string {
	if s.All {
		return ""
	}
	if s.CustomerID == "" {
		return "00000000-0000-0000-0000-000000000000"
	}
	return s.CustomerID
}

// supportSafePayload keeps only identifiers and state names of a staff-made audit entry; reasons, notes, emails,
// addresses and any other free text are dropped (AD-4).
func supportSafePayload(raw []byte) []byte {
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		return []byte(`{}`)
	}
	out := map[string]any{}
	for _, k := range []string{"appliance_id", "site_id", "customer_id", "license_id", "serial", "state", "from_state", "to_state"} {
		if v, ok := in[k].(string); ok {
			out[k] = v
		}
	}
	b, _ := json.Marshal(out)
	return b
}

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

// canManageForCustomer: a platform permission covers every customer, a customer.* permission only the session's own.
func canManageForCustomer(s *auth.Session, customerID, platformPerm, customerPerm string) bool {
	if s == nil || customerID == "" {
		return false
	}
	if s.HasPermission(platformPerm) {
		return true
	}
	return s.HasPermission(customerPerm) && s.DefaultTenantID == customerID
}

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

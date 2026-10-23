package api

// An appliance still holding a customer's data is never activated for a different customer. A new identity key
// is not proof of a factory reset (an identity reset keeps the site database), so the appliance reports
// holds_customer_id in its signed registration and offline request; activate refuses any other customer,
// including one created inline or one since deleted from Central. The same customer (breakglass recovery) is allowed.

import (
	"net/http"
	"regexp"
	"strings"
)

const (
	codeHoldsOtherCustomerData = "holds_other_customer_data"
	msgHoldsOtherCustomerData  = "This appliance still holds another customer's data. Factory-reset it before " +
		"activating it for a different customer."
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// normalizeHeldCustomer accepts "" or a uuid (returned lower-case); ok=false otherwise.
func normalizeHeldCustomer(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", true
	}
	if !uuidRe.MatchString(v) {
		return "", false
	}
	return strings.ToLower(v), true
}

// holdsOtherCustomerRefusal returns nil when an appliance holding heldCustomerID ("" = none) may be activated for
// targetCustomerID ("" = a customer created inline, always a different one).
func holdsOtherCustomerRefusal(heldCustomerID, targetCustomerID string) *moveRefusal {
	if heldCustomerID == "" {
		return nil
	}
	if targetCustomerID != "" && strings.EqualFold(heldCustomerID, targetCustomerID) {
		return nil
	}
	return &moveRefusal{Status: http.StatusConflict, Code: codeHoldsOtherCustomerData, Message: msgHoldsOtherCustomerData}
}

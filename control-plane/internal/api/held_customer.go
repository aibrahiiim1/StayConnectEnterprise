package api

// AN APPLIANCE THAT STILL HOLDS ANOTHER CUSTOMER'S DATA IS NEVER ACTIVATED FOR A DIFFERENT CUSTOMER.
//
// Product-Owner rule: changing an appliance's customer is retire -> FACTORY-CLEAN -> new activation. A new
// identity key is not proof of a factory reset: a support identity reset keeps the site database and still
// produces a new key. So the appliance reports, inside its signed registration and its signed offline
// activation request, the customer whose data it still holds (holds_customer_id). Central stores it on the
// appliance (appliances.held_customer_id, migration 0048) and activate refuses any other customer -- including
// a new one created inline, and including when the held customer has since been deleted from Central: the
// data is still on the box. Activation for the same customer (breakglass recovery) is allowed.

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

// normalizeHeldCustomer validates an appliance-reported holds_customer_id: "" (holds none) or a uuid, returned
// lower-case. ok=false for anything else.
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

// holdsOtherCustomerRefusal decides whether an appliance holding heldCustomerID ("" = none) may be activated
// for targetCustomerID ("" = a customer being created inline, which is always a different customer). nil
// means go ahead.
func holdsOtherCustomerRefusal(heldCustomerID, targetCustomerID string) *moveRefusal {
	if heldCustomerID == "" {
		return nil
	}
	if targetCustomerID != "" && strings.EqualFold(heldCustomerID, targetCustomerID) {
		return nil
	}
	return &moveRefusal{Status: http.StatusConflict, Code: codeHoldsOtherCustomerData, Message: msgHoldsOtherCustomerData}
}

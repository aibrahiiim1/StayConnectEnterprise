package main

// THE PRIVILEGE SURFACE OF GUEST ACTIVITY.
//
// iam_v2.auth_contexts is the authentication record, not a lookup table. Every row carries exactly one
// subject -- a stay, a voucher, a guest account, a guest principal or a post-stay profile -- plus the device
// and the network the guest came in on. Migration 0083 grants svc_edged column-level SELECT on five columns
// and no others, precisely so that this screen can name a room without being able to correlate a person's
// credential across every authentication the appliance has performed.
//
// A column-level grant is invisible in the Go source: adding `ac.voucher_id` to the query compiles, reviews
// as a one-word change, and fails at runtime with "permission denied for table auth_contexts" -- on the
// appliance, in front of an operator. This test makes that a build-time failure instead, and states the
// reason next to the list so the next person knows widening it is a database decision rather than a query
// edit.

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// grantedAuthContextColumns is migrations/0083. Keep the two in step; the database is the authority.
var grantedAuthContextColumns = map[string]bool{
	"id": true, "tenant_id": true, "site_id": true, "stay_id": true, "pms_interface_id": true,
}

func TestGuestActivityReadsOnlyTheAuthContextColumnsItWasGranted(t *testing.T) {
	src, err := os.ReadFile("resources_commerce.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)

	body := s[strings.Index(s, "func (s *server) listGuestActivity("):]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "iam_v2.auth_contexts") {
		t.Fatal("guest activity no longer reads auth_contexts; an untaken offer cannot name its stay without it")
	}

	// Every `ac.<column>` the query mentions must be one the migration granted.
	var forbidden []string
	for _, m := range regexp.MustCompile(`\bac\.([a-z_]+)`).FindAllStringSubmatch(body, -1) {
		if !grantedAuthContextColumns[m[1]] {
			forbidden = append(forbidden, m[1])
		}
	}
	sort.Strings(forbidden)
	if len(forbidden) > 0 {
		t.Errorf("the query reads auth_contexts column(s) %v that svc_edged was not granted. "+
			"This is a PRIVILEGE change, not a query change: widening it means a new migration and a "+
			"Product-Owner decision, because those columns are the credential a guest presented",
			forbidden)
	}

	// The tenant/site isolation on the join is deliberate: matching on id alone would trust the foreign key
	// to be the only thing keeping one site's quotes away from another site's contexts.
	if !strings.Contains(body, "ac.tenant_id = q.tenant_id") || !strings.Contains(body, "ac.site_id = q.site_id") {
		t.Error("the auth_contexts join no longer asserts tenant and site isolation")
	}
}

func TestAnUntakenOfferStillResolvesThroughTheAuthContext(t *testing.T) {
	// The purchase path answers for TAKEN offers only. If a later edit drops the fallback, the screen
	// silently goes back to a blank room on exactly the rows an operator opened it to investigate -- and no
	// behavioural test would notice, because every offer on a healthy appliance tends to have been taken.
	src, _ := os.ReadFile("resources_commerce.go")
	body := string(src)
	body = body[strings.Index(body, "func (s *server) listGuestActivity("):]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}

	if !strings.Contains(body, "COALESCE(p.stay_id, ac.stay_id)") {
		t.Error("the stay is no longer resolved from the auth context when there is no purchase")
	}
	// The interface must follow the same fallback, or a room could be shown without the namespace that gives
	// it meaning -- which is the one thing a room number must never be shown without.
	if !strings.Contains(body, "COALESCE(st.pms_interface_id, ac.pms_interface_id, q.pms_interface_id)") {
		t.Error("the PMS interface does not follow the same fallback as the stay; a room would lose its scope")
	}
	// And nothing may invent one.
	for _, guess := range []string{"ORDER BY abs(", "INTERVAL '", "closest", "nearest"} {
		if strings.Contains(body, guess) {
			t.Errorf("the query contains %q, which looks like attribution inferred from timing", guess)
		}
	}
}

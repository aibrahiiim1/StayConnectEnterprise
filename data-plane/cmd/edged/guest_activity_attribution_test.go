package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// grantedAuthContextColumns mirrors the column-level SELECT grant on auth_contexts (migration 0083), which the Go source cannot show.
// Widening it is a database privilege decision, not a query edit.
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

	// Tenant/site isolation on the join is deliberate: id alone would rely on the foreign key.
	if !strings.Contains(body, "ac.tenant_id = q.tenant_id") || !strings.Contains(body, "ac.site_id = q.site_id") {
		t.Error("the auth_contexts join no longer asserts tenant and site isolation")
	}
}

func TestAnUntakenOfferStillResolvesThroughTheAuthContext(t *testing.T) {
	// The purchase path covers taken offers only; dropping the fallback blanks the room on untaken ones, which no behavioural test would notice.
	src, _ := os.ReadFile("resources_commerce.go")
	body := string(src)
	body = body[strings.Index(body, "func (s *server) listGuestActivity("):]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}

	if !strings.Contains(body, "COALESCE(p.stay_id, ac.stay_id)") {
		t.Error("the stay is no longer resolved from the auth context when there is no purchase")
	}
	// The interface must follow the same fallback: a room number is never shown without its namespace.
	if !strings.Contains(body, "COALESCE(st.pms_interface_id, ac.pms_interface_id, q.pms_interface_id)") {
		t.Error("the PMS interface does not follow the same fallback as the stay; a room would lose its scope")
	}
	// Attribution must never be inferred from timing.
	for _, guess := range []string{"ORDER BY abs(", "INTERVAL '", "closest", "nearest"} {
		if strings.Contains(body, guess) {
			t.Errorf("the query contains %q, which looks like attribution inferred from timing", guess)
		}
	}
}

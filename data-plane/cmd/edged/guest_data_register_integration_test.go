//go:build integration

package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestIntegration_GuestDataRegister_DeadlineAnswerAndRemoval(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	who := "Zed Registrant " + sfx
	received := time.Now().UTC().AddDate(0, 0, -5).Format("2006-01-02")

	st, body := f.do(t, "POST", "/guest-data-rights/register", map[string]any{
		"kind": "ACCESS", "received_at": received, "requester": who, "verification_method": "ID_CHECKED_IN_PERSON"})
	if st != 201 {
		t.Fatalf("open = %d %v", st, body)
	}
	id, _ := body["id"].(string)
	if !strings.HasPrefix(fmt.Sprint(body["ref"]), "RQ-") || id == "" {
		t.Fatalf("no reference: %v", body)
	}

	if st, _ := f.do(t, "POST", "/guest-data-rights/register", map[string]any{"kind": "ACCESS", "received_at": time.Now().UTC().AddDate(0, 0, 3).Format("2006-01-02"), "requester": who}); st == 201 {
		t.Errorf("a request from the future was recorded")
	}
	if st, _ := f.do(t, "POST", "/guest-data-rights/register", map[string]any{"kind": "BOGUS", "received_at": received, "requester": who}); st != 400 {
		t.Errorf("unknown kind = %d", st)
	}
	if st, _ := f.do(t, "POST", "/guest-data-rights/register", map[string]any{"kind": "ACCESS", "received_at": received, "requester": ""}); st == 201 {
		t.Errorf("a request with no requester was recorded")
	}

	st, list := f.do(t, "GET", "/guest-data-rights/register", nil)
	if st != 200 || !strings.Contains(fmt.Sprint(list), who) {
		t.Fatalf("list = %d %v", st, list)
	}
	if st, b := f.do(t, "POST", "/guest-data-rights/register/"+id+"/extend", map[string]any{"reason": "complex, several stays"}); st != 200 {
		t.Fatalf("extend = %d %v", st, b)
	}
	if st, _ := f.do(t, "POST", "/guest-data-rights/register/"+id+"/extend", map[string]any{"reason": "complex, several stays"}); st != 409 {
		t.Errorf("a second extension = %d, want 409", st)
	}

	if st, _ := f.do(t, "POST", "/guest-data-rights/register/"+id+"/answer", map[string]any{"status": "REFUSED", "outcome": "NONE"}); st == 200 {
		t.Errorf("a refusal without a ground was accepted")
	}
	st, ans := f.do(t, "POST", "/guest-data-rights/register/"+id+"/answer", map[string]any{
		"status": "PARTLY_REFUSED", "outcome": "LEGAL_RETENTION", "note": "invoice data kept by law"})
	if st != 200 || ans["requester"] != who || ans["answered_in_time"] != true {
		t.Fatalf("answer = %d %v", st, ans)
	}

	if n := gdStr(t, f, `SELECT requester_ref FROM iam_v2.guest_data_request_register WHERE id=$1`, id); n != "[removed]" {
		t.Errorf("requester kept: %q", n)
	}
	if st, _ := f.do(t, "POST", "/guest-data-rights/register/"+id+"/answer", map[string]any{"status": "ANSWERED"}); st != 409 {
		t.Errorf("answering twice = %d, want 409", st)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE iam_v2.guest_data_request_register SET received_at = received_at - 1 WHERE id=$1`, id); err == nil {
		t.Errorf("an answered request could be edited")
	}
	if _, err := f.pool.Exec(t.Context(), `DELETE FROM iam_v2.guest_data_request_register WHERE id=$1`, id); err == nil {
		t.Errorf("a request could be deleted")
	}
}

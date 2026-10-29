//go:build integration

package main

import (
	"strings"
	"testing"
)

// AD-8 (P9 L2): the desk cannot replace a client's password without a bounded reason, and the reason reaches the audit entry.
func TestIntegration_SetPassword_NeedsABoundedReason(t *testing.T) {
	f := newAPI(t)
	st, body := f.do(t, "POST", "/guest-accounts/", map[string]any{"username": "reasoned", "password": "pw-one"})
	if st != 201 {
		t.Fatalf("create = %d %v", st, body)
	}
	id := body["account"].(map[string]any)["id"].(string)
	for name, reason := range map[string]string{"missing": "", "blank": "    ", "too short": "abc", "too long": strings.Repeat("x", 501)} {
		b := map[string]any{"password": "pw-two", "disconnect_sessions": false}
		if reason != "" || name == "blank" {
			b["reason"] = reason
		}
		st, resp := f.do(t, "POST", "/guest-accounts/"+id+"/set-password", b)
		if st != 400 || resp["error"] != "reason_required" {
			t.Errorf("%s reason: status %d %v, want 400 reason_required", name, st, resp)
		}
	}
	st, resp := f.do(t, "POST", "/guest-accounts/"+id+"/set-password",
		map[string]any{"password": "pw-two", "disconnect_sessions": false, "reason": "  client forgot it at the desk  "})
	if st != 200 || resp["status"] != "password_set" {
		t.Fatalf("with a reason = %d %v", st, resp)
	}
	var got string
	if err := f.pool.QueryRow(t.Context(), `SELECT payload->>'reason' FROM public.audit_log
	      WHERE action='guest_account.password_set' AND target_id=$1 ORDER BY ts DESC LIMIT 1`, id).Scan(&got); err != nil {
		t.Fatalf("audit: %v", err)
	}
	if got != "client forgot it at the desk" {
		t.Errorf("the audit entry carries reason %q", got)
	}
}

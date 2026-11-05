package authctx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE DEPARTED-STAY RULE IS ONE RULE: issue_or_return_pms_context (0209) and the consume here must admit exactly the same stays.
func TestAfterCheckoutLiveMatchesTheContextFunction(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0209_a_departed_guest_with_after_checkout_access_can_sign_in_again.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	squash := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	if !strings.Contains(squash(string(b)), squash(AfterCheckoutLive)) {
		t.Fatalf("the 0209 context function no longer contains AfterCheckoutLive word for word:\n%s", AfterCheckoutLive)
	}
	if !strings.Contains(squash(string(b)), "OR (st.status='CHECKED_OUT' AND "+squash(AfterCheckoutLive)) {
		t.Fatal("0209 admits a departed stay under a different status condition")
	}
	for _, must := range []string{"e.status = 'ACTIVE'", "e.end_mode = 'GRACE_AFTER_CHECKOUT'", "e.window_ends_at > now()", "e.stay_id = st.id"} {
		if !strings.Contains(AfterCheckoutLive, must) {
			t.Errorf("AfterCheckoutLive lost %q", must)
		}
	}
}

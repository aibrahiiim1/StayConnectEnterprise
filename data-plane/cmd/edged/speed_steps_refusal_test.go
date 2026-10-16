package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// O-5: an operator saving a package with speed steps -- more than one grant step, or a step that sets speeds --
// is refused with a sentence that says why and what to do, before anything is published. A single empty step
// still saves exactly as before (that path is TestAddWithATakenCodeAnswers409CodeExists).
func TestSavingSpeedStepsIsRefusedWithAReason(t *testing.T) {
	s := commerceTestServer(t, &fakeCommerceRepo{tx: &fakeCommerceTx{}})
	for _, tiers := range []string{
		`[{"order":10,"grant":{}},{"order":20,"grant":{"down_kbps":2000,"up_kbps":500}}]`,
		`[{"order":10,"grant":{"down_kbps":2000}}]`,
	} {
		body := `{"code":"GOLD","service_plan_revision_id":"r","duration_policy":{"end_mode":"MANUAL_END"},"grant_tiers":` + tiers + `}`
		w := httptest.NewRecorder()
		s.publishCommercialPackage(w, httptest.NewRequest(http.MethodPost, "/commercial-packages", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: want 400, got %d %s", tiers, w.Code, w.Body.String())
		}
		var out map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if out["error"] != "validation" || !strings.HasPrefix(out["message"], "speed_steps_not_supported") ||
			!strings.Contains(out["message"], "service plan") {
			t.Fatalf("%s: refusal %v", tiers, out)
		}
	}
}

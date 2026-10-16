package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// P4: paging the audit log loses no row when several rows share a timestamp (audit_log has no id column).
func TestAuditCursorKeepsSameInstantRows(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 5; i++ {
		if _, err := f.pool.Exec(f.ctx, `INSERT INTO audit_log (ts, actor_type, action, target_type, target_id, payload)
            VALUES ('2026-10-16T10:00:00Z','system','test.same_instant','thing',$1,'{}')`, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 10; page++ {
		u := "/cloud/v1/audit?action=test&limit=2"
		if cursor != "" {
			u += "&cursor=" + url.QueryEscape(cursor)
		}
		req := httptest.NewRequest(http.MethodGet, u, nil)
		req = req.WithContext(auth.ContextWithSession(req.Context(), f.session))
		rec := httptest.NewRecorder()
		f.base.listAudit(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("audit: %d %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Items []struct {
				TargetID string `json:"target_id"`
			} `json:"items"`
			Next *string `json:"next_cursor"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		for _, it := range out.Items {
			if seen[it.TargetID] {
				t.Fatalf("row %s returned twice", it.TargetID)
			}
			seen[it.TargetID] = true
		}
		if out.Next == nil {
			break
		}
		cursor = *out.Next
	}
	if len(seen) != 5 {
		t.Fatalf("paging returned %d of 5 same-instant rows", len(seen))
	}
}

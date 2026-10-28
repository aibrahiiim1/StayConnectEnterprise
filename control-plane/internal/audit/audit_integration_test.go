package audit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stayconnect/enterprise/control-plane/internal/testdb"
)

// Audit Phase 1 M14: a cancelled request context still writes the audit row of a committed change, and Op
// leaves the caller's map unchanged.
func TestAuditWritesAfterCancelAndKeepsCallerMap(t *testing.T) {
	pool := testdb.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	payload := map[string]any{"_tenant_id": "", "reason": "kept"}
	req := httptest.NewRequest(http.MethodPost, "/x", nil).WithContext(ctx)
	Op(ctx, pool, req, "test.cancelled_context", "thing", "t1", payload)
	if _, ok := payload["_tenant_id"]; !ok || payload["reason"] != "kept" {
		t.Fatalf("Op changed the caller's map: %v", payload)
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action='test.cancelled_context'
        AND payload->>'reason'='kept' AND NOT payload ? '_tenant_id'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("the audit row must be written despite the cancelled context, got %d", n)
	}
}

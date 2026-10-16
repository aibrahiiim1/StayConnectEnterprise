package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/control-plane/internal/testdb"
)

// X-10: failed appliance authentications are audited within a budget; a flood from one address writes at
// most 5 rows a minute instead of one per request.
func TestApplianceAuthFailuresAreAuditedWithinABudget(t *testing.T) {
	pool := testdb.New(t)
	h := RequireAppliance(pool, applianceauth.NewReplayCache(time.Minute, 100))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for i := 0; i < 20; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/appliance/hello", nil)
		req.RemoteAddr = "198.51.100.40:5555"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("no JWT: want 401, got %d", rec.Code)
		}
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action='appliance.auth_failed'
        AND host(ip)='198.51.100.40'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("one address may write 5 auth-failure rows per minute, wrote %d", n)
	}
}

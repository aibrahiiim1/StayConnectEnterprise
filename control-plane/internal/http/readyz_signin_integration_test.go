package http

import (
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/testdb"
)

// central-deploy.sh's smoke test requires "signin":"available", so a release without the two-step key is rolled back.
func TestReadyzReportsWhetherSignInIsAvailable(t *testing.T) {
	pool := testdb.New(t)
	url := os.Getenv("CTRLAPI_TEST_REDIS_URL")
	if url == "" {
		t.Skip("CTRLAPI_TEST_REDIS_URL is not set: this test needs a Redis")
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opt)
	defer rdb.Close()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	mfa, err := auth.NewMFA(pool, key)
	if err != nil {
		t.Fatal(err)
	}
	ready := func(m *auth.MFA) string {
		rec := httptest.NewRecorder()
		readyz(Deps{DB: pool, Redis: rdb, MFA: m, Version: "test"})(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("readyz = %d %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	if b := ready(mfa); !strings.Contains(b, `"signin":"available"`) || !strings.Contains(b, `"ready"`) {
		t.Fatalf("with the key: %s", b)
	}
	if b := ready(nil); !strings.Contains(b, `"signin":"unavailable"`) {
		t.Fatalf("without the key: %s", b)
	}
}

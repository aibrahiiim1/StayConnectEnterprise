package signinattempt

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"testing"
)

// P2 L10: a store without a database enforces nothing; it says so in the log instead of silently allowing.
func TestStoreWithoutDatabaseIsLogged(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)
	mac, _ := net.ParseMAC("02:00:00:00:00:01")
	var s *ProtectionStore
	if g, err := s.Check(context.Background(), "t", "s", mac); err != nil || g.Restricted {
		t.Fatalf("nil store: %+v %v", g, err)
	}
	if !strings.Contains(buf.String(), "NOT enforced") {
		t.Fatalf("a disabled store was not logged: %q", buf.String())
	}
}

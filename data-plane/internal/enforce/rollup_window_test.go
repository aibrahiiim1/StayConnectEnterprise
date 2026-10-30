package enforce

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestNextRollupWindow(t *testing.T) {
	timeout := &pgconn.PgError{Code: "57014"}
	lockTimeout := &pgconn.PgError{Code: "55P03"}
	cases := []struct {
		name string
		cur  time.Duration
		took time.Duration
		err  error
		want time.Duration
	}{
		{"statement timeout shrinks to a quarter", 2 * time.Hour, 30 * time.Second, timeout, 30 * time.Minute},
		{"repeated timeouts stop at the one-minute minimum", 2 * time.Minute, 30 * time.Second, timeout, time.Minute},
		{"already at the minimum stays", time.Minute, 30 * time.Second, timeout, time.Minute},
		{"a quick success widens", 15 * time.Minute, time.Second, nil, 30 * time.Minute},
		{"widening stops at two hours", 2 * time.Hour, time.Second, nil, 2 * time.Hour},
		{"a slow success keeps the window", 30 * time.Minute, 12 * time.Second, nil, 30 * time.Minute},
		{"a lock timeout is not a size problem", 2 * time.Hour, 5 * time.Second, lockTimeout, 2 * time.Hour},
		{"a lost connection is not a size problem", 30 * time.Minute, time.Second, errors.New("EOF"), 30 * time.Minute},
	}
	for _, c := range cases {
		if got := nextRollupWindow(c.cur, c.took, c.err); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	// From two hours, four cancelled statements reach the minimum: 2h, 30m, 7.5m, 1m52s, 1m.
	w := rollupWindowMax
	for i := 0; i < 4; i++ {
		w = nextRollupWindow(w, 30*time.Second, timeout)
	}
	if w != rollupWindowMin {
		t.Fatalf("after four cancelled statements the window is %v, want %v", w, rollupWindowMin)
	}
}

func TestRollupWindowsAreKeptPerSite(t *testing.T) {
	var w rollupWindows
	if got := w.get("a"); got != rollupWindowMax {
		t.Fatalf("a new site starts at %v", got)
	}
	w.set("a", 5*time.Minute)
	if w.get("a") != 5*time.Minute || w.get("b") != rollupWindowMax {
		t.Fatalf("windows are not independent per site")
	}
}

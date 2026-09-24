package pmsrest

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// sleepRecorder replaces real waiting so no contract test sleeps.
type sleepRecorder struct{ waits []time.Duration }

func (s *sleepRecorder) sleep(_ context.Context, d time.Duration) error {
	s.waits = append(s.waits, d)
	return nil
}

var fixedNow = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func testOptions(t *testing.T, tz string, rec *sleepRecorder) Options {
	t.Helper()
	loc, err := time.LoadLocation(tz)
	if err != nil {
		t.Fatal(err)
	}
	return Options{HTTP: &http.Client{Timeout: 5 * time.Second}, Now: func() time.Time { return fixedNow },
		Location: loc, Sleep: rec.sleep}
}

func withPageSize(t *testing.T, p *int, n int) {
	t.Helper()
	old := *p
	*p = n
	t.Cleanup(func() { *p = old })
}

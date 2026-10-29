package main

import (
	"net/url"
	"testing"
	"time"
)

// AD-56 (a): 30 days by default, older history on request, no hard maximum.
func TestParseListWindow(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

	since, info, err := parseListWindow(url.Values{}, now)
	if err != nil || since == nil || !since.Equal(now.AddDate(0, 0, -30)) || info.All || info.DefaultDays != 30 {
		t.Fatalf("default = %v %+v %v, want now-30d", since, info, err)
	}

	since, info, err = parseListWindow(url.Values{"window": {"all"}}, now)
	if err != nil || since != nil || !info.All || info.Since != nil {
		t.Fatalf("window=all = %v %+v %v, want the whole history", since, info, err)
	}

	// no hard maximum: a start many years back is accepted as asked
	since, _, err = parseListWindow(url.Values{"since": {"2019-01-01T00:00:00Z"}}, now)
	if err != nil || since == nil || since.Year() != 2019 {
		t.Fatalf("since = %v %v", since, err)
	}

	for _, bad := range []url.Values{{"since": {"yesterday"}}, {"window": {"forever"}}} {
		if _, _, err := parseListWindow(bad, now); err == nil {
			t.Fatalf("accepted %v; a period other than the one asked for must never be shown silently", bad)
		}
	}
}

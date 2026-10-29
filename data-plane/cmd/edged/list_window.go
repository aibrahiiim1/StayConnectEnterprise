package main

// Default list window (audit AD-56 (a)): the ended-sessions tab, the all-sessions tab and the audit list showed ALL history
// and aggregated it on every view. They now show the last 30 days by default; older history is asked for, with
// ?window=all (the whole history) or ?since=<RFC 3339> (from that time). There is no hard maximum.
//
// The response says what window it applied (listWindowInfo) so a screen can offer "show older".

import (
	"errors"
	"net/url"
	"time"
)

const defaultListWindowDays = 30

type listWindowInfo struct {
	// Since is the start of the window applied; nil when the whole history is shown.
	Since *time.Time `json:"since"`
	// All is true when the caller asked for the whole history.
	All bool `json:"all"`
	// DefaultDays is the window used when the caller asks for nothing.
	DefaultDays int `json:"default_days"`
}

// parseListWindow reads ?window=all and ?since=. since is nil for the whole history. An unparseable ?since is an
// error (a different period than the one asked for is never shown silently).
func parseListWindow(q url.Values, now time.Time) (since *time.Time, info listWindowInfo, err error) {
	info = listWindowInfo{DefaultDays: defaultListWindowDays}
	if q.Get("window") == "all" {
		info.All = true
		return nil, info, nil
	}
	if w := q.Get("window"); w != "" {
		return nil, info, errors.New("window must be \"all\" or omitted")
	}
	t := now.UTC().AddDate(0, 0, -defaultListWindowDays)
	if v := q.Get("since"); v != "" {
		p, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			return nil, info, errors.New("since must be an RFC 3339 time")
		}
		t = p.UTC()
	}
	info.Since = &t
	return &t, info, nil
}

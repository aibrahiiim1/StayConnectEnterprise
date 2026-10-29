package main

// AD-35: when scd answers "busy" (503 with Retry-After: it is at its request cap, or every password-hash slot stayed taken
// for two seconds), the portal waits that long, up to busyRetryMax, and asks ONCE more on the guest's behalf. A second
// "busy" is passed through and the guest is told the service is busy. Only that answer is retried: scd said nothing was
// done, so repeating the request cannot double anything. Any other 503, an error or a timeout is never retried.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"
)

const busyRetryMax = 3 * time.Second

type busyRetry struct {
	next  http.RoundTripper
	sleep func(time.Duration) // tests replace it
}

func (b *busyRetry) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := b.next.RoundTrip(r)
	if err != nil || resp.StatusCode != http.StatusServiceUnavailable {
		return resp, err
	}
	wait, ok := retryAfter(resp)
	if !ok {
		return resp, nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil || e.Error != "BUSY" || (r.Body != nil && r.GetBody == nil) {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp, nil
	}
	if wait > busyRetryMax {
		wait = busyRetryMax
	}
	sleep := b.sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	select {
	case <-r.Context().Done():
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp, nil
	default:
	}
	sleep(wait)
	r2 := r.Clone(r.Context())
	if r.GetBody != nil {
		nb, gerr := r.GetBody()
		if gerr != nil {
			resp.Body = io.NopCloser(bytes.NewReader(body))
			return resp, nil
		}
		r2.Body = nb
	}
	resp2, err2 := b.next.RoundTrip(r2)
	if err2 != nil {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp, nil
	}
	return resp2, nil
}

func retryAfter(resp *http.Response) (time.Duration, bool) {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, false
	}
	return time.Duration(n) * time.Second, true
}

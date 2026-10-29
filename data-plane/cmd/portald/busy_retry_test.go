package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type scriptedScd struct {
	replies []*http.Response
	calls   int
	bodies  []string
}

func (s *scriptedScd) RoundTrip(r *http.Request) (*http.Response, error) {
	b := ""
	if r.Body != nil {
		raw, _ := io.ReadAll(r.Body)
		b = string(raw)
	}
	s.bodies = append(s.bodies, b)
	i := s.calls
	if i >= len(s.replies) {
		i = len(s.replies) - 1
	}
	s.calls++
	return s.replies[i], nil
}

func reply(code int, retryAfter, body string) *http.Response {
	h := http.Header{"Content-Type": {"application/json"}}
	if retryAfter != "" {
		h.Set("Retry-After", retryAfter)
	}
	return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

func post(t *testing.T, rt http.RoundTripper, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", "http://unix/v1/sessions/authorize", bytes.NewReader([]byte(body)))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// AD-35: a "busy" answer is retried once, after the stated wait (at most busyRetryMax), with the same body.
func TestBusyIsRetriedOnceAfterTheStatedWait(t *testing.T) {
	scd := &scriptedScd{replies: []*http.Response{
		reply(503, "2", `{"error":"BUSY","retry_after":2}`),
		reply(200, "", `{"ok":true}`),
	}}
	var slept []time.Duration
	rt := &busyRetry{next: scd, sleep: func(d time.Duration) { slept = append(slept, d) }}
	resp := post(t, rt, `{"voucher":"X"}`)
	if resp.StatusCode != 200 || scd.calls != 2 {
		t.Fatalf("status %d after %d calls, want 200 after 2", resp.StatusCode, scd.calls)
	}
	if len(slept) != 1 || slept[0] != 2*time.Second {
		t.Fatalf("waited %v, want one 2s wait", slept)
	}
	if scd.bodies[0] != scd.bodies[1] || scd.bodies[1] != `{"voucher":"X"}` {
		t.Fatalf("the retry did not carry the same body: %q", scd.bodies)
	}
}

func TestASecondBusyIsPassedThroughAndNotRetriedAgain(t *testing.T) {
	scd := &scriptedScd{replies: []*http.Response{reply(503, "2", `{"error":"BUSY"}`)}}
	// reply() bodies are single-use; give each call its own.
	scd.replies = []*http.Response{reply(503, "2", `{"error":"BUSY"}`), reply(503, "2", `{"error":"BUSY"}`), reply(503, "2", `{"error":"BUSY"}`)}
	rt := &busyRetry{next: scd, sleep: func(time.Duration) {}}
	resp := post(t, rt, `{}`)
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 503 || !strings.Contains(string(b), "BUSY") || scd.calls != 2 {
		t.Fatalf("status %d body %s calls %d, want a passed-through 503 BUSY after exactly 2 calls", resp.StatusCode, b, scd.calls)
	}
}

func TestOnlyBusyIsRetried(t *testing.T) {
	for name, r := range map[string]*http.Response{
		"an outage without a wait":    reply(503, "", `{"error":"IAMV2_UNAVAILABLE"}`),
		"a 503 with a wait, not busy": reply(503, "5", `{"error":"IAMV2_UNAVAILABLE"}`),
		"a refusal":                   reply(429, "30", `{"error":"TOO_MANY_ATTEMPTS"}`),
	} {
		scd := &scriptedScd{replies: []*http.Response{r}}
		rt := &busyRetry{next: scd, sleep: func(time.Duration) { t.Fatalf("%s was waited on", name) }}
		resp := post(t, rt, `{}`)
		if scd.calls != 1 {
			t.Fatalf("%s: %d calls, want 1", name, scd.calls)
		}
		if b, _ := io.ReadAll(resp.Body); len(b) == 0 {
			t.Fatalf("%s: the body was lost", name)
		}
	}
}

func TestTheWaitIsCapped(t *testing.T) {
	scd := &scriptedScd{replies: []*http.Response{reply(503, "60", `{"error":"BUSY"}`), reply(200, "", `{}`)}}
	var slept time.Duration
	rt := &busyRetry{next: scd, sleep: func(d time.Duration) { slept = d }}
	post(t, rt, `{}`)
	if slept != busyRetryMax {
		t.Fatalf("waited %v, want the cap %v", slept, busyRetryMax)
	}
}

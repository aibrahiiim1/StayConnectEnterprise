package main

// A guest whose package ran out is told, and told nothing else: no guest, room, reservation or PMS identity leaks.

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type scdSaying struct{ reason string }

func (s scdSaying) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := json.Marshal(map[string]string{"ended_reason": s.reason})
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    r,
	}, nil
}

func statusFixture(t *testing.T, reason string) *handler {
	t.Helper()
	h := &handler{scd: &http.Client{Transport: scdSaying{reason: reason}}, clock: newRecordingClock()}
	h.arpCache = func(net.IP) (net.HardwareAddr, bool) {
		mac, _ := net.ParseMAC("02:00:00:00:70:01")
		return mac, true
	}
	return h
}

func askStatus(t *testing.T, h *handler) (int, accessStatusOut, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/access/status", strings.NewReader("{}"))
	req.RemoteAddr = "10.7.7.7:41000"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.accessStatus(rec, req)
	var out accessStatusOut
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func TestAccessStatus_DataExhaustionIsExplained(t *testing.T) {
	code, out, _ := askStatus(t, statusFixture(t, "DATA"))
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if out.EndedReason != "DATA" {
		t.Fatalf("reason = %q, want DATA", out.EndedReason)
	}
	if out.Message != "Your Internet package has ended because the data allowance was used." {
		t.Fatalf("message = %q", out.Message)
	}
}

func TestAccessStatus_TimeExpiryIsExplained(t *testing.T) {
	_, out, _ := askStatus(t, statusFixture(t, "TIME"))
	if out.EndedReason != "TIME" {
		t.Fatalf("reason = %q, want TIME", out.EndedReason)
	}
	if out.Message != "Your Internet package has ended because the access time expired." {
		t.Fatalf("message = %q", out.Message)
	}
}

// A device that has not just run out gets no notice, not an error.
func TestAccessStatus_NothingToSayIsSilentAndSuccessful(t *testing.T) {
	for _, reason := range []string{"", "SOMETHING_ELSE", "ADDRESS_NO_LONGER_OWNED", "ENTITLEMENT_ENDED"} {
		code, out, _ := askStatus(t, statusFixture(t, reason))
		if code != http.StatusOK {
			t.Fatalf("%q: status %d — a device with no notice is not an error", reason, code)
		}
		if out.Message != "" || out.EndedReason != "" {
			t.Fatalf("%q produced a notice: %+v", reason, out)
		}
	}
}

func TestAccessStatus_AnUnavailableLookupIsSilent(t *testing.T) {
	h := &handler{scd: &http.Client{Transport: darkScd{}}, clock: newRecordingClock()}
	h.arpCache = func(net.IP) (net.HardwareAddr, bool) {
		mac, _ := net.ParseMAC("02:00:00:00:70:01")
		return mac, true
	}
	code, out, _ := askStatus(t, h)
	if code != http.StatusOK || out.Message != "" {
		t.Fatalf("an unavailable lookup was not silent: %d %+v", code, out)
	}
}

func TestAccessStatus_UnknownDeviceGetsNoNotice(t *testing.T) {
	h := statusFixture(t, "DATA")
	h.arpCache = func(net.IP) (net.HardwareAddr, bool) { return nil, false }
	code, out, _ := askStatus(t, h)
	if code != http.StatusOK || out.Message != "" {
		t.Fatalf("unknown device produced %d %+v", code, out)
	}
}

func TestAccessStatus_LeaksNoGuestOrPMSIdentity(t *testing.T) {
	for _, reason := range []string{"DATA", "TIME"} {
		_, _, raw := askStatus(t, statusFixture(t, reason))
		low := strings.ToLower(raw)
		for _, forbidden := range []string{
			"room", "stay", "guest", "reservation", "folio", "pms", "entitlement", "session",
			"mac", "device_id", "voucher", "account", "name", "bytes", "quota",
		} {
			if strings.Contains(low, forbidden) {
				t.Fatalf("the %s notice disclosed %q: %s", reason, forbidden, raw)
			}
		}
	}
}

// The two sentences are the complete vocabulary; a new reason needs a deliberate wording here.
func TestAccessStatus_MessageVocabularyIsClosed(t *testing.T) {
	if len(endedMessages) != 2 {
		t.Fatalf("the guest-facing vocabulary grew to %d entries without review", len(endedMessages))
	}
	for _, k := range []string{"DATA", "TIME"} {
		if endedMessages[k] == "" {
			t.Fatalf("no sentence for %s", k)
		}
	}
}

// AD-57 (c): a "nothing to say" answer is remembered per client address for a few seconds so a reload does not reach scd
// again, and a notice is never remembered.
type countingScd struct {
	reason string
	calls  int
}

func (c *countingScd) RoundTrip(r *http.Request) (*http.Response, error) {
	c.calls++
	return scdSaying{reason: c.reason}.RoundTrip(r)
}

func TestAccessStatus_ANegativeAnswerIsRemembered_ANoticeIsNot(t *testing.T) {
	quiet := &countingScd{reason: ""}
	h := statusFixture(t, "")
	h.scd = &http.Client{Transport: quiet}
	h.accessQuiet = newQuietCache()
	for i := 0; i < 5; i++ {
		if _, out, _ := askStatus(t, h); out.EndedReason != "" {
			t.Fatalf("unexpected notice %+v", out)
		}
	}
	if quiet.calls != 1 {
		t.Fatalf("scd was asked %d times for five views from one address, want 1", quiet.calls)
	}

	notice := &countingScd{reason: "DATA"}
	h2 := statusFixture(t, "DATA")
	h2.scd = &http.Client{Transport: notice}
	h2.accessQuiet = newQuietCache()
	for i := 0; i < 3; i++ {
		if _, out, _ := askStatus(t, h2); out.EndedReason != "DATA" {
			t.Fatalf("view %d lost the notice: %+v", i, out)
		}
	}
	if notice.calls != 3 {
		t.Fatalf("a notice must be asked every time; scd was asked %d times", notice.calls)
	}
}

func TestQuietCacheExpires(t *testing.T) {
	c := newQuietCache()
	now := c.now()
	c.now = func() time.Time { return now }
	c.remember("10.0.0.1")
	if !c.quiet("10.0.0.1") {
		t.Fatal("not remembered")
	}
	now = now.Add(accessStatusQuietFor + time.Second)
	if c.quiet("10.0.0.1") {
		t.Fatal("still remembered after the window")
	}
}

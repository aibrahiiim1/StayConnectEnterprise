package main

// A GUEST WHOSE PACKAGE RAN OUT MUST BE TOLD, AND TOLD NOTHING ELSE.
//
// Two sentences, chosen by the server, shown above an unchanged sign-in form. These cases pin both halves:
// the message appears for DATA and TIME, and the surface leaks no guest, room, reservation or PMS identity —
// which is the whole reason the answer is a bounded word rather than a description of what ended.

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// scdSaying is an scd that answers the access-status hop with one reason.
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

// THE ORDINARY CASE. A device that has not just run out gets no notice at all — not an error, because an
// error here would appear on the first connection of every guest's stay.
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

// scd being unreachable must not break the page: the sign-in form is what the guest needs either way.
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

// A device the appliance cannot place on its own neighbour table gets no notice, and no error either.
func TestAccessStatus_UnknownDeviceGetsNoNotice(t *testing.T) {
	h := statusFixture(t, "DATA")
	h.arpCache = func(net.IP) (net.HardwareAddr, bool) { return nil, false }
	code, out, _ := askStatus(t, h)
	if code != http.StatusOK || out.Message != "" {
		t.Fatalf("unknown device produced %d %+v", code, out)
	}
}

// THE DISCLOSURE RULE. The whole point of answering with a bounded word is that nothing else can ride along.
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

// The two sentences are the complete vocabulary. A new reason must not be able to reach a guest without
// somebody choosing its wording here.
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

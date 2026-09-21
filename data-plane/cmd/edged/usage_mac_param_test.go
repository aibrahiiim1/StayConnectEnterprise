package main

// THE DEVICE LOOKUP MUST ACCEPT THE ADDRESS THE BROWSER ACTUALLY SENDS.
//
// Found on the appliance by an operator, not by a test. The Usage Explorer's per-stay view showed a device
// with twelve sessions and 576 MB of traffic; searching for that same address in the per-device view
// answered "this appliance has no record of that device".
//
// A MAC contains colons, a colon is reserved in a URL path, and the screen correctly escapes it with
// encodeURIComponent -- so the server received 7e%3A58%3A… , chi handed back the raw escaped segment, and
// `$1::macaddr` failed to cast. The client was right, the database was right, and the server was reading an
// encoded string as though it were a literal one. The refusal then blamed the device, because a cast error
// and an empty result had been given the same answer.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// macRequest returns a request whose {mac} route parameter is the given raw path segment, exactly as chi
// would present it to the handler after routing.
func macRequest(segment string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/usage/devices/placeholder", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("mac", segment)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestEveryFormAClientMightSendResolvesToOneAddress(t *testing.T) {
	const want = "7a:1b:2c:3d:4e:5f"

	for _, tc := range []struct{ name, segment string }{
		{"literal colons, as curl sends", "7a:1b:2c:3d:4e:5f"},
		{"percent-encoded, as encodeURIComponent sends", "7a%3A1b%3A2c%3A3d%3A4e%3A5f"},
		{"uppercase", "7A:1B:2C:3D:4E:5F"},
		{"uppercase and encoded", "7A%3A1B%3A2C%3A3D%3A4E%3A5F"},
		{"dashes, as pasted from a Windows tool", "7a-1b-2c-3d-4e-5f"},
		{"surrounding whitespace, encoded", "%207a:1b:2c:3d:4e:5f%20"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := macFromPath(macRequest(tc.segment))
			if !ok {
				t.Fatalf("%q was rejected; the browser sends the encoded form and it must be accepted", tc.segment)
			}
			if got != want {
				t.Errorf("%q normalised to %q, want %q — the database stores one form", tc.segment, got, want)
			}
		})
	}
}

func TestSomethingThatIsNotAnAddressIsNotReportedAsAnUnknownDevice(t *testing.T) {
	// "No record of that device" is a claim ABOUT A DEVICE. Saying it when the caller never named one is how
	// the original defect stayed invisible: the message blamed the data for a fault in the request.
	for _, bad := range []string{
		"", "%20", "not-a-mac", "7a:1b:2c:3d:4e", "../../etc/passwd", "%zz", "7a:1b:2c:3d:4e:5g",
	} {
		if got, ok := macFromPath(macRequest(bad)); ok {
			t.Errorf("%q was accepted as a device address and became %q", bad, got)
		}
	}
}

func TestAnEightByteAddressIsRefusedRatherThanFailingInPostgres(t *testing.T) {
	// net.ParseMAC accepts EUI-64 and Infiniband addresses. PostgreSQL's macaddr holds six bytes, so one of
	// those would parse here and fail the cast later -- which is the exact shape of the bug being fixed.
	if got, ok := macFromPath(macRequest("7a:1b:2c:3d:4e:5f:60:71")); ok {
		t.Errorf("an eight-byte address was accepted as %q; macaddr cannot store it", got)
	}
}

func TestTheHandlerSeparatesItsThreeFailures(t *testing.T) {
	// A malformed request, an absent row and a broken query are three different answers. They were one.
	src := readSourceFile(t, "resources_usage.go")
	body := src[strings.Index(src, "func (s *server) getDeviceUsage("):]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}

	for _, needed := range []string{
		`http.StatusBadRequest, "bad_mac"`,           // you did not give me an address
		`errors.Is(err, pgx.ErrNoRows)`,              // I have no record of it
		`http.StatusInternalServerError, "internal"`, // I could not look
	} {
		if !strings.Contains(body, needed) {
			t.Errorf("getDeviceUsage no longer distinguishes %s", needed)
		}
	}
	if strings.Contains(body, `chi.URLParam(r, "mac")`) {
		t.Error("the handler reads the raw path parameter again instead of decoding it through macFromPath")
	}
}

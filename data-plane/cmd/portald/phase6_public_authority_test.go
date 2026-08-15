package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// THE PUBLIC LIST/RELEASE AUTHORITY PATH, END TO END, WITH TWO REAL SUBJECTS.
//
// The source-identity tests next door prove that no forwarding header can move what clientIP() resolves.
// That is the mechanism. This file proves the CONSEQUENCE, which is the thing that actually matters: that a
// guest who spoofs another guest's address cannot see or release that guest's devices.
//
// The difference is not academic. clientIP() could be perfectly correct and the flow still broken -- if a
// handler consulted the header separately, if the derived device were overridden downstream, if the release
// target were resolved against something other than the caller's own entitlement. Only a test that walks the
// whole path with two distinct subjects can tell those apart, so this one does:
//
//   caller A  10.90.0.11 / 02:00:00:aa:00:11 -> entitlement ent-A, devices dev-A1 (online), dev-A2 (offline)
//   victim B  10.90.0.22 / 02:00:00:bb:00:22 -> entitlement ent-B, devices dev-B1 (offline)
//
// Requests go through h.routes() -- the REAL router with the REAL middleware stack -- and the fake scd is
// written to model the appliance's actual authority rule: it resolves the subject from the device identity
// the portal derived, and scopes every lookup and every mutation to that subject. If portald ever forwarded
// a guest-influenced identity, the fake would faithfully act on it, and these tests would fail by RELEASING
// the victim's device rather than by some assertion about headers.

// ---- a fake scd that models the real authority rule ------------------------------------------------------

type p6FakeDevice struct {
	id       string
	online   bool
	released bool
}

type p6Subject struct {
	mac     string
	devices []*p6FakeDevice
}

type p6FakeScd struct {
	subjects []*p6Subject
	// seenMACs records the device identity portald forwarded on each hop, so a test can assert what the
	// downstream service was actually told rather than only what it answered.
	seenMACs []string
	calls    []string
}

func (f *p6FakeScd) bySubjectMAC(mac string) *p6Subject {
	for _, s := range f.subjects {
		if strings.EqualFold(s.mac, mac) {
			return s
		}
	}
	return nil
}

func (f *p6FakeScd) RoundTrip(req *http.Request) (*http.Response, error) {
	var body struct {
		Device   map[string]string `json:"device"`
		DeviceID string            `json:"device_id"`
	}
	_ = json.NewDecoder(req.Body).Decode(&body)
	f.calls = append(f.calls, req.URL.Path)
	f.seenMACs = append(f.seenMACs, body.Device["mac"])

	reply := func(v any) (*http.Response, error) {
		raw, _ := json.Marshal(v)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(raw)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	}
	unavailable := map[string]any{"outcome": "UNAVAILABLE"}

	// THE SUBJECT COMES FROM THE FORWARDED DEVICE IDENTITY AND FROM NOTHING ELSE — exactly as scd resolves
	// it from its own tables. There is no branch here that consults a header or a body field, because there
	// is none in the real service either.
	subj := f.bySubjectMAC(body.Device["mac"])
	if subj == nil {
		return reply(unavailable)
	}

	if strings.HasSuffix(req.URL.Path, "/list") {
		out := []map[string]any{}
		for _, d := range subj.devices {
			if d.released {
				continue
			}
			out = append(out, map[string]any{
				"id": d.id, "last_seen": "2026-08-15T09:00:00Z",
				"online": d.online, "removable": !d.online,
			})
		}
		return reply(map[string]any{"outcome": "LISTED", "devices": out})
	}

	// RELEASE is scoped to the caller's OWN devices. A target belonging to another subject is not "denied";
	// it simply matches nothing, which is why it answers exactly like an id that never existed.
	for _, d := range subj.devices {
		if d.id != body.DeviceID || d.released || d.online {
			continue
		}
		d.released = true
		return reply(map[string]any{"outcome": "RELEASED"})
	}
	return reply(unavailable)
}

// ---- fixtures --------------------------------------------------------------------------------------------

const (
	p6CallerIP  = "10.90.0.11"
	p6CallerMAC = "02:00:00:aa:00:11"
	p6VictimIP  = "10.90.0.22"
	p6VictimMAC = "02:00:00:bb:00:22"
)

func p6Fixture(t *testing.T) (*handler, *p6FakeScd) {
	t.Helper()
	fake := &p6FakeScd{subjects: []*p6Subject{
		{mac: p6CallerMAC, devices: []*p6FakeDevice{
			{id: "dev-A1", online: true},
			{id: "dev-A2", online: false},
		}},
		{mac: p6VictimMAC, devices: []*p6FakeDevice{
			{id: "dev-B1", online: false},
		}},
	}}
	h := &handler{scd: &http.Client{Transport: fake}, clock: newRecordingClock()}
	// The appliance's neighbour table: each address belongs to a DIFFERENT device. This is the fixture that
	// makes the test meaningful -- if both addresses resolved to one MAC, spoofing would be undetectable
	// because there would be only one subject.
	h.arpCache = func(ip net.IP) (net.HardwareAddr, bool) {
		switch ip.String() {
		case p6CallerIP:
			mac, _ := net.ParseMAC(p6CallerMAC)
			return mac, true
		case p6VictimIP:
			mac, _ := net.ParseMAC(p6VictimMAC)
			return mac, true
		}
		return nil, false
	}
	return h, fake
}

// p6Call drives the REAL router, from the real peer `from`, carrying whatever headers the caller invents.
func p6Call(t *testing.T, h *handler, path, from string, headers map[string]string, body string) deviceOut {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.RemoteAddr = from + ":41000"
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.routes().ServeHTTP(rec, req)

	var out deviceOut
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		// Every non-success on this surface is the uniform guest envelope, which does not carry `ok`. That
		// decodes into deviceOut with OK=false, which is what the callers below assert on.
		return deviceOut{}
	}
	return out
}

func p6DeviceIDs(out deviceOut) []string {
	ids := make([]string, 0, len(out.Devices))
	for _, d := range out.Devices {
		ids = append(ids, d.ID)
	}
	sort.Strings(ids)
	return ids
}

func (f *p6FakeScd) device(t *testing.T, id string) *p6FakeDevice {
	t.Helper()
	for _, s := range f.subjects {
		for _, d := range s.devices {
			if d.id == id {
				return d
			}
		}
	}
	t.Fatalf("no such fixture device %q", id)
	return nil
}

// ---- the proofs ------------------------------------------------------------------------------------------

// LIST never exposes another guest's devices, whatever the caller claims their address is.
func TestPhase6ListNeverExposesAnotherGuestsDevices(t *testing.T) {
	for _, header := range spoofHeaders {
		t.Run(header, func(t *testing.T) {
			h, fake := p6Fixture(t)
			out := p6Call(t, h, "/devices/list", p6CallerIP, map[string]string{header: p6VictimIP}, `{}`)
			if !out.OK {
				t.Fatalf("the caller's own LIST failed, so this proves nothing: %+v", out)
			}
			got := p6DeviceIDs(out)
			for _, id := range got {
				if strings.HasPrefix(id, "dev-B") {
					t.Fatalf("%s exposed victim B's device %s to caller A", header, id)
				}
			}
			if len(got) != 2 || got[0] != "dev-A1" || got[1] != "dev-A2" {
				t.Fatalf("%s changed which devices the caller sees: %v", header, got)
			}
			// ...and the identity scd was told is the caller's own, derived from the connection.
			if len(fake.seenMACs) != 1 || !strings.EqualFold(fake.seenMACs[0], p6CallerMAC) {
				t.Fatalf("%s: scd was handed %v, not the caller's derived identity", header, fake.seenMACs)
			}
		})
	}
}

// A multi-hop chain, several headers at once, and the victim's address in every one of them.
func TestPhase6ListIgnoresACombinedSpoof(t *testing.T) {
	h, fake := p6Fixture(t)
	out := p6Call(t, h, "/devices/list", p6CallerIP, map[string]string{
		"X-Forwarded-For": p6VictimIP + ", 203.0.113.9",
		"X-Real-IP":       p6VictimIP,
		"True-Client-IP":  p6VictimIP,
		"Forwarded":       "for=" + p6VictimIP,
	}, `{}`)
	if got := p6DeviceIDs(out); len(got) != 2 || got[0] != "dev-A1" {
		t.Fatalf("a combined spoof changed the listed subject: %v", got)
	}
	if !strings.EqualFold(fake.seenMACs[0], p6CallerMAC) {
		t.Fatalf("a combined spoof changed the forwarded identity: %v", fake.seenMACs)
	}
}

// THE ONE THAT WOULD ACTUALLY HURT. A spoofing caller must not be able to knock another guest's device off
// the network -- and the assertion is on the victim's DURABLE state, not on the answer the attacker got.
func TestPhase6ReleaseNeverTouchesAnotherGuestsDevice(t *testing.T) {
	for _, header := range spoofHeaders {
		t.Run(header, func(t *testing.T) {
			h, fake := p6Fixture(t)
			out := p6Call(t, h, "/devices/release", p6CallerIP,
				map[string]string{header: p6VictimIP}, `{"device_id":"dev-B1"}`)
			if out.OK {
				t.Fatalf("%s: releasing another guest's device REPORTED SUCCESS", header)
			}
			if fake.device(t, "dev-B1").released {
				t.Fatalf("%s: victim B's device was released by caller A", header)
			}
			if len(fake.seenMACs) == 1 && !strings.EqualFold(fake.seenMACs[0], p6CallerMAC) {
				t.Fatalf("%s: scd was handed %v, not the caller's derived identity", header, fake.seenMACs)
			}
		})
	}
}

// Naming the victim's device WITHOUT a spoof header is the same non-answer: the target is resolved inside
// the caller's own subject, so an id belonging to somebody else matches nothing.
func TestPhase6ReleaseOfAForeignDeviceIDIsTheSameNonAnswer(t *testing.T) {
	h, fake := p6Fixture(t)
	foreign := p6Call(t, h, "/devices/release", p6CallerIP, nil, `{"device_id":"dev-B1"}`)
	if foreign.OK {
		t.Fatal("a foreign device id was accepted")
	}
	if fake.device(t, "dev-B1").released {
		t.Fatal("a foreign device id released the victim's device")
	}

	h2, _ := p6Fixture(t)
	invented := p6Call(t, h2, "/devices/release", p6CallerIP, nil, `{"device_id":"dev-does-not-exist"}`)
	// Indistinguishable: "not yours" and "never existed" must be the same answer, or the surface is an
	// oracle for which device ids are real.
	if invented.OK != foreign.OK || invented.Message != foreign.Message {
		t.Fatalf("a foreign id (%+v) is distinguishable from an invented one (%+v)", foreign, invented)
	}
}

// The genuine flow still works — otherwise every refusal above would be satisfied by a surface that refuses
// everything.
func TestPhase6CallerAOperatesOnItsOwnDevices(t *testing.T) {
	h, fake := p6Fixture(t)

	listed := p6Call(t, h, "/devices/list", p6CallerIP, nil, `{}`)
	if got := p6DeviceIDs(listed); len(got) != 2 {
		t.Fatalf("the caller could not list their own devices: %v", got)
	}
	// The online device is presented as not removable; the offline one is.
	for _, d := range listed.Devices {
		if d.ID == "dev-A1" && d.Removable {
			t.Fatal("an ONLINE device was presented as removable")
		}
		if d.ID == "dev-A2" && !d.Removable {
			t.Fatal("the caller's offline device was not presented as removable")
		}
	}

	released := p6Call(t, h, "/devices/release", p6CallerIP, nil, `{"device_id":"dev-A2"}`)
	if !released.OK {
		t.Fatalf("the caller could not release their OWN offline device: %+v", released)
	}
	if !fake.device(t, "dev-A2").released {
		t.Fatal("the release reported success without releasing anything")
	}
	// ...and it is gone from their next listing, while the online one stays.
	after := p6DeviceIDs(p6Call(t, h, "/devices/list", p6CallerIP, nil, `{}`))
	if len(after) != 1 || after[0] != "dev-A1" {
		t.Fatalf("the listing after a release is %v", after)
	}

	// The caller's own ONLINE device is not removable through the flow either.
	online := p6Call(t, h, "/devices/release", p6CallerIP, nil, `{"device_id":"dev-A1"}`)
	if online.OK || fake.device(t, "dev-A1").released {
		t.Fatal("an ONLINE device was released")
	}
}

// Victim B resolves independently and still holds their own device — which is what proves the subjects are
// genuinely separate rather than one shared pool that nobody managed to reach.
func TestPhase6VictimBResolvesIndependently(t *testing.T) {
	h, fake := p6Fixture(t)
	// A spoofs B, and fails.
	_ = p6Call(t, h, "/devices/release", p6CallerIP, map[string]string{"X-Forwarded-For": p6VictimIP},
		`{"device_id":"dev-B1"}`)

	// B connects genuinely and sees their own device, intact.
	out := p6Call(t, h, "/devices/list", p6VictimIP, nil, `{}`)
	if got := p6DeviceIDs(out); len(got) != 1 || got[0] != "dev-B1" {
		t.Fatalf("victim B does not see their own device: %v", got)
	}
	if fake.device(t, "dev-B1").released {
		t.Fatal("victim B's device was released")
	}
	// ...and B can release it themselves. The device was never protected by being unreachable; it was
	// protected by belonging to B.
	if got := p6Call(t, h, "/devices/release", p6VictimIP, nil, `{"device_id":"dev-B1"}`); !got.OK {
		t.Fatalf("victim B could not release their own device: %+v", got)
	}
	if !fake.device(t, "dev-B1").released {
		t.Fatal("B's own release did nothing")
	}
	// A never reached B's subject at any point.
	for _, mac := range fake.seenMACs[:1] {
		if strings.EqualFold(mac, p6VictimMAC) {
			t.Fatal("caller A's request was forwarded with victim B's identity")
		}
	}
}

// A device that is not on the guest network at all gets the uniform answer and never reaches scd -- so the
// surface cannot be used from off-network to probe whether it exists.
func TestPhase6UnknownPeerNeverReachesScd(t *testing.T) {
	h, fake := p6Fixture(t)
	out := p6Call(t, h, "/devices/list", "10.99.0.99", map[string]string{"X-Real-IP": p6CallerIP}, `{}`)
	if out.OK {
		t.Fatal("an unknown peer was served")
	}
	if len(fake.calls) != 0 {
		t.Fatalf("an unknown peer reached scd: %v", fake.calls)
	}
}

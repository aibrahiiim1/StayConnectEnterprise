package main

// An empty DHCP lease table is the one answer that means "Kea has lost its leases" as easily as "nobody holds an address". Reading it as the second
// ended every session at once. These tests pin that an untrusted empty answer is UNKNOWN (access is not renewed, no session is ended, nothing about
// the Entitlement or quota is touched) while a trusted one, and a table that names other holders, keep the established behaviour.

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// provenWriter is a writer whose session is already proven active in durable state, so only the address-ownership decision can end it.
func provenWriter(src leaseSource) (*fakeGate, *fakeEnforcement, *phase3Shaping) {
	tc, g, gens, origins, enf, _ := sysSetup()
	p := sysWriter(tc, g, gens, origins, enf)
	enf.state["s-1"] = "active"
	p.owner = addressOwner{src: src}
	return g, enf, p
}

// planAt is a plan generated and valid at the given instant, since the pass is judged against the time it is submitted at.
func planAt(gen int64, at time.Time, sessions ...shapeplan.Session) shapeplan.Envelope {
	env := envelope(gen, sessions...)
	env.GeneratedAt, env.ExpiresAt = at.UTC(), at.UTC().Add(90*time.Second)
	return env
}

type stabilityLeases struct {
	rows    []KeaLease
	err     error
	trusted bool
	asked   int
	epoch   time.Time // when the Kea instance that answers started
}

func (s *stabilityLeases) Leases() ([]KeaLease, error) { return s.rows, s.err }
func (s *stabilityLeases) KeaEpoch() (time.Time, bool) { return s.epoch, !s.epoch.IsZero() }
func (s *stabilityLeases) AbsenceTrusted(net.IP) (bool, string) {
	s.asked++
	return s.trusted, "Kea started 3 s ago (less than 300 s)"
}

// A lease verified earlier stays valid on the client's side until it lapses, whatever Kea now remembers: the client renews at half its lease time,
// which can be long after a restart. The address therefore stays authorized for exactly the rest of that lease and no longer.
func TestRememberedLeaseBridgesALostLeaseTable(t *testing.T) {
	src := &stabilityLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}}
	g, enf, p := provenWriter(src)
	s := sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")
	base := time.Now()
	if res, err := p.submit(context.Background(), envelope(1, s), base); err != nil || res.Shaped != 1 {
		t.Fatalf("setup: %+v %v", res, err)
	}

	src.rows = nil // Kea restarted onto an empty lease file
	for i, at := range []time.Duration{30 * time.Second, 5 * time.Minute, 9 * time.Minute} {
		g.advance(30 * time.Second)
		res, err := p.submit(context.Background(), planAt(int64(2+i), base.Add(at), s), base.Add(at))
		if err != nil || res.Shaped != 1 {
			t.Fatalf("at +%s the valid session was not kept: %+v %v", at, res, err)
		}
	}
	if len(enf.ended) != 0 || !g.isAuthorized("br-guest", "10.0.0.5") {
		t.Fatalf("a valid session was ended or cut off: ended=%v", enf.ended)
	}
	if p.unverifiedCount() != 0 {
		t.Fatal("a session bridged by its remembered lease is reported unverified")
	}
}

func TestRememberedLeaseNeverOutlivesTheLease(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		src := &stabilityLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}}
		g, enf, p := provenWriter(src)
		s := sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")
		base := time.Now()
		if _, err := p.submit(context.Background(), envelope(1, s), base); err != nil {
			t.Fatal(err)
		}
		src.rows, src.trusted = nil, trusted
		asked := len(g.requestedTTLs("br-guest", "10.0.0.5"))

		// 595 s into a 600 s lease, with the installed element about to lapse: a renewal may grant no more than the 5 s that remain.
		g.advance(88 * time.Second)
		if _, err := p.submit(context.Background(), planAt(2, base.Add(595*time.Second), s), base.Add(595*time.Second)); err != nil {
			t.Fatal(err)
		}
		ttls := g.requestedTTLs("br-guest", "10.0.0.5")
		if len(ttls) <= asked {
			t.Fatalf("trusted=%v: no renewal was requested", trusted)
		}
		if last := ttls[len(ttls)-1]; last > 5*time.Second {
			t.Fatalf("trusted=%v: %s was granted with 5 s of lease left", trusted, last)
		}

		// Past the lease the memory is gone and the table's own verdict applies.
		g.advance(30 * time.Second)
		res, err := p.submit(context.Background(), planAt(3, base.Add(11*time.Minute), s), base.Add(11*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if trusted {
			if len(enf.ended) != 1 || g.isAuthorized("br-guest", "10.0.0.5") {
				t.Fatalf("a lapsed lease on a trusted empty table was not withdrawn: ended=%v", enf.ended)
			}
		} else if res.Shaped != 0 || len(enf.ended) != 0 || p.unverifiedCount() != 1 {
			t.Fatalf("a lapsed lease on an untrusted table must withhold renewal and end nothing: %+v ended=%v", res, enf.ended)
		}
	}
}

// Even a very long lease is only bridged for leaseMemoryCap: Kea having lost its table is no licence to keep an address for days.
func TestRememberedLeaseIsCapped(t *testing.T) {
	long := KeaLease{IPAddress: "10.0.0.5", HWAddr: "96:48:f9:7a:9b:09", ValidLft: 86400, CLTT: time.Now().Unix()}
	src := &stabilityLeases{rows: []KeaLease{long}}
	g, enf, p := provenWriter(src)
	s := sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")
	base := time.Now()
	if _, err := p.submit(context.Background(), envelope(1, s), base); err != nil {
		t.Fatal(err)
	}
	src.rows = nil
	g.advance(30 * time.Second)
	if res, _ := p.submit(context.Background(), planAt(2, base.Add(3*time.Hour), s), base.Add(3*time.Hour)); res.Shaped != 1 {
		t.Fatalf("inside the cap the session should be kept: %+v", res)
	}
	g.advance(30 * time.Second)
	res, _ := p.submit(context.Background(), planAt(3, base.Add(leaseMemoryCap+time.Minute), s), base.Add(leaseMemoryCap+time.Minute))
	if res.Shaped != 0 || len(enf.ended) != 0 {
		t.Fatalf("beyond the cap the address must stop being renewed (and not be ended on an untrusted table): %+v ended=%v", res, enf.ended)
	}
}

// After a restart of netd itself nothing is remembered: an untrusted empty table then withholds renewal and ends nothing.
func TestEmptyUntrustedTableWithNothingRememberedEndsNothingAndRenewsNothing(t *testing.T) {
	src := &stabilityLeases{trusted: false}
	g, enf, p := provenWriter(src)
	s := sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")
	res, err := p.submit(context.Background(), envelope(1, s), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 0 || len(enf.ended) != 0 || p.unverifiedCount() != 1 {
		t.Fatalf("%+v ended=%v unverified=%d", res, enf.ended, p.unverifiedCount())
	}
	if g.isAuthorized("br-guest", "10.0.0.5") {
		t.Fatal("an address nobody verified was authorized")
	}
	noted := false
	for _, m := range res.Problems {
		if contains(m, "NOT renewed") {
			noted = true
		}
	}
	if !noted {
		t.Fatalf("the withheld renewal was not reported: %+v", res.Problems)
	}
}

func TestEmptyTrustedTableWithNothingRememberedWithdrawsTheAddress(t *testing.T) {
	src := &stabilityLeases{trusted: true}
	g, enf, p := provenWriter(src)
	s := sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")
	if _, err := p.submit(context.Background(), envelope(1, s), time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(enf.ended) != 1 || g.isAuthorized("br-guest", "10.0.0.5") {
		t.Fatalf("ended=%v", enf.ended)
	}
}

// A table that names OTHER holders is real evidence that this address was given up; a remembered lease does not override it.
func TestTableNamingOtherHoldersIsBelievedDespiteARememberedLease(t *testing.T) {
	src := &stabilityLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}}
	_, enf, p := provenWriter(src)
	s := sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")
	base := time.Now()
	if _, err := p.submit(context.Background(), envelope(1, s), base); err != nil {
		t.Fatal(err)
	}
	src.rows = []KeaLease{lease("10.0.0.5", "aa:bb:cc:dd:ee:ff")}
	if _, err := p.submit(context.Background(), planAt(2, base.Add(time.Minute), s), base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if src.asked != 0 || len(enf.ended) != 1 {
		t.Fatalf("asked=%d ended=%v", src.asked, enf.ended)
	}
}

func TestOwnsReturnsUnknownNotForeignForAnUntrustedEmptyTable(t *testing.T) {
	o := addressOwner{src: &stabilityLeases{trusted: false}}
	if got := o.Owns(context.Background(), net.ParseIP("10.0.0.5"), "96:48:f9:7a:9b:09"); got != addressUnknown {
		t.Fatalf("Owns = %v, want addressUnknown", got)
	}
	o = addressOwner{src: &stabilityLeases{trusted: true}}
	if got := o.Owns(context.Background(), net.ParseIP("10.0.0.5"), "96:48:f9:7a:9b:09"); got != addressForeign {
		t.Fatalf("Owns = %v, want addressForeign", got)
	}
	// A source that cannot judge stability keeps the established meaning of an empty table.
	o = addressOwner{src: fakeLeases{}}
	if got := o.Owns(context.Background(), net.ParseIP("10.0.0.5"), "96:48:f9:7a:9b:09"); got != addressForeign {
		t.Fatalf("Owns = %v, want addressForeign for a plain source", got)
	}
}

// ---- the real client against a fake Kea control socket ----------------------------------------------------------------------------

type fakeKeaServer struct {
	mu       sync.Mutex
	commands []string
	status   map[string]any // arguments of status-get
	statusOK int
	subnets  []string
}

func serveFakeKea(t *testing.T, srv *fakeKeaServer) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "k.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("unix sockets are not available here: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				raw, _ := io.ReadAll(c)
				var req struct {
					Command string `json:"command"`
				}
				_ = json.Unmarshal(raw, &req)
				srv.mu.Lock()
				srv.commands = append(srv.commands, req.Command)
				srv.mu.Unlock()
				var resp any
				switch req.Command {
				case "status-get":
					resp = []any{map[string]any{"result": srv.statusOK, "arguments": srv.status}}
				case "config-get":
					var sn []any
					for _, s := range srv.subnets {
						sn = append(sn, map[string]any{"subnet": s})
					}
					resp = []any{map[string]any{"result": 0, "arguments": map[string]any{"Dhcp4": map[string]any{"subnet4": sn}}}}
				default:
					resp = []any{map[string]any{"result": 0}}
				}
				out, _ := json.Marshal(resp)
				_, _ = c.Write(out)
			}(c)
		}
	}()
	return path
}

func TestKeaEmptyAnswerIsTrustedOnlyWhenStableAndServingTheSubnet(t *testing.T) {
	ip := net.ParseIP("10.20.0.50")
	cases := []struct {
		name    string
		srv     *fakeKeaServer
		trusted bool
	}{
		{"just started", &fakeKeaServer{status: map[string]any{"uptime": 3.0, "reload": 3.0}, subnets: []string{"10.20.0.0/22"}}, false},
		{"reconfigured a moment ago", &fakeKeaServer{status: map[string]any{"uptime": 99999.0, "reload": 20.0}, subnets: []string{"10.20.0.0/22"}}, false},
		{"stable but the subnet is not served", &fakeKeaServer{status: map[string]any{"uptime": 99999.0, "reload": 99999.0}, subnets: []string{"10.30.0.0/22"}}, false},
		{"does not say how long it has run", &fakeKeaServer{status: map[string]any{}, subnets: []string{"10.20.0.0/22"}}, false},
		{"status-get fails", &fakeKeaServer{statusOK: 1, status: map[string]any{}, subnets: []string{"10.20.0.0/22"}}, false},
		{"stable and serving the subnet", &fakeKeaServer{status: map[string]any{"uptime": 99999.0, "reload": 99999.0}, subnets: []string{"10.20.0.0/22"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k := newKeaClient(serveFakeKea(t, tc.srv))
			got, why := k.AbsenceTrusted(ip)
			if got != tc.trusted {
				t.Fatalf("trusted=%v (%s), want %v", got, why, tc.trusted)
			}
			if !got && why == "" {
				t.Fatal("an untrusted answer gave no reason")
			}
		})
	}
	// An unreachable control socket is never trusted either.
	if got, _ := newKeaClient(filepath.Join(t.TempDir(), "absent.sock")).AbsenceTrusted(ip); got {
		t.Fatal("an unreachable Kea was trusted")
	}
}

// ConfigSet pushes the live configuration and does NOT ask Kea to write it to disk: what a cold Kea starts from is netd's own write of the confirmed
// configuration (kea_persist.go), never whatever happens to be live.
func TestConfigSetDoesNotAskKeaToWriteTheFile(t *testing.T) {
	srv := &fakeKeaServer{}
	k := newKeaClient(serveFakeKea(t, srv))
	if err := k.ConfigSet(map[string]any{"subnet4": []any{}}); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.commands) != 1 || srv.commands[0] != "config-set" {
		t.Fatalf("commands sent: %v", srv.commands)
	}
	_ = errors.New
}

func epochOf(base time.Time, ago time.Duration) time.Time {
	return base.Add(-ago).Truncate(time.Second)
}

// Once one client has renewed into a restarted Kea the table is no longer empty, yet every client that has not renewed yet is missing from it. They
// are the same clients with valid leases and must not be ended for that.
func TestClientsThatHaveNotRenewedIntoARestartedKeaAreKept(t *testing.T) {
	base := time.Now()
	src := &stabilityLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}, epoch: epochOf(base, 2*time.Hour)}
	g, enf, p := provenWriter(src)
	s := sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")
	if _, err := p.submit(context.Background(), planAt(1, base, s), base); err != nil {
		t.Fatal(err)
	}

	// Kea restarted onto a file that only knows a neighbour.
	src.rows = []KeaLease{lease("10.0.0.9", "aa:bb:cc:dd:ee:ff")}
	src.epoch = epochOf(base, 30*time.Second)
	for i, at := range []time.Duration{time.Minute, 9 * time.Minute} {
		g.advance(30 * time.Second)
		res, err := p.submit(context.Background(), planAt(int64(2+i), base.Add(at), s), base.Add(at))
		if err != nil || res.Shaped != 1 {
			t.Fatalf("at +%s a client with a valid lease was not kept: %+v %v", at, res, err)
		}
	}
	if len(enf.ended) != 0 {
		t.Fatalf("sessions were ended: %v", enf.ended)
	}
}

// With the same Kea instance still running, no lease means the client released it or it expired: the address is withdrawn, as it always was.
func TestAMissingLeaseFromTheSameKeaInstanceIsStillAWithdrawal(t *testing.T) {
	base := time.Now()
	src := &stabilityLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}, epoch: epochOf(base, 2*time.Hour), trusted: true}
	g, enf, p := provenWriter(src)
	s := sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")
	if _, err := p.submit(context.Background(), planAt(1, base, s), base); err != nil {
		t.Fatal(err)
	}
	src.rows = []KeaLease{lease("10.0.0.9", "aa:bb:cc:dd:ee:ff")} // ours was released
	g.advance(30 * time.Second)
	if _, err := p.submit(context.Background(), planAt(2, base.Add(time.Minute), s), base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(enf.ended) != 1 || g.isAuthorized("br-guest", "10.0.0.5") {
		t.Fatalf("a released lease kept its address: ended=%v", enf.ended)
	}
}

// A Kea that was only just reconfigured is not believed about an absence either, but the same instance does not bridge: renewal is withheld, nothing ends.
func TestAnAbsenceFromAnUnsettledKeaOfTheSameInstanceWithholdsRenewalOnly(t *testing.T) {
	base := time.Now()
	src := &stabilityLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}, epoch: epochOf(base, 2*time.Hour)}
	_, enf, p := provenWriter(src)
	s := sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")
	if _, err := p.submit(context.Background(), planAt(1, base, s), base); err != nil {
		t.Fatal(err)
	}
	src.rows, src.trusted = []KeaLease{lease("10.0.0.9", "aa:bb:cc:dd:ee:ff")}, false
	res, err := p.submit(context.Background(), planAt(2, base.Add(time.Minute), s), base.Add(time.Minute))
	if err != nil || res.Shaped != 0 || len(enf.ended) != 0 || p.unverifiedCount() != 1 {
		t.Fatalf("%+v ended=%v unverified=%d (%v)", res, enf.ended, p.unverifiedCount(), err)
	}
}

// The remembered lease covers one address and one device. A session that now has another address or another device is not covered by it.
func TestRememberedLeaseIsBoundToTheAddressAndTheDevice(t *testing.T) {
	cases := []struct {
		name string
		s    func(s shapeplan.Session) shapeplan.Session
		ip   string
	}{
		{"another address", func(s shapeplan.Session) shapeplan.Session { s.IP = "10.0.0.6"; return s }, "10.0.0.6"},
		{"another device", func(s shapeplan.Session) shapeplan.Session { s.MAC = "aa:bb:cc:dd:ee:ff"; return s }, "10.0.0.5"},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			src := &stabilityLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}}
			g, enf, p := provenWriter(src)
			s := sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")
			base := time.Now()
			if _, err := p.submit(context.Background(), planAt(1, base, s), base); err != nil {
				t.Fatal(err)
			}
			src.rows = nil
			g.advance(30 * time.Second)
			moved := tc.s(s)
			res, _ := p.submit(context.Background(), planAt(2, base.Add(time.Minute), moved), base.Add(time.Minute))
			if res.Shaped != 0 {
				t.Fatalf("a lease verified for something else covered %s: %+v", tc.name, res)
			}
			if tc.ip != "10.0.0.5" && g.isAuthorized("br-guest", tc.ip) {
				t.Fatalf("the new address %s was authorized on the old address's lease", tc.ip)
			}
			_ = enf
		})
	}
}

// Positive evidence that the address was reassigned ends the session at once, remembered lease or not.
func TestAReassignedAddressEndsTheSessionDespiteTheMemory(t *testing.T) {
	base := time.Now()
	src := &stabilityLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}, epoch: epochOf(base, 2*time.Hour)}
	g, enf, p := provenWriter(src)
	s := sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")
	if _, err := p.submit(context.Background(), planAt(1, base, s), base); err != nil {
		t.Fatal(err)
	}
	src.epoch = epochOf(base, 20*time.Second) // Kea restarted
	src.rows = []KeaLease{lease("10.0.0.5", "aa:bb:cc:dd:ee:ff")}
	g.advance(30 * time.Second)
	if _, err := p.submit(context.Background(), planAt(2, base.Add(time.Minute), s), base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(enf.ended) != 1 || g.isAuthorized("br-guest", "10.0.0.5") {
		t.Fatalf("another device holds the address and the session survived: ended=%v", enf.ended)
	}
}

func TestKeaRestartDetectionToleratesClockJitterOnly(t *testing.T) {
	t0 := time.Now().Truncate(time.Second)
	if keaRestarted(t0, t0.Add(3*time.Second)) || keaRestarted(t0, t0.Add(-3*time.Second)) {
		t.Fatal("uptime rounding was read as a restart")
	}
	if !keaRestarted(t0, t0.Add(2*time.Minute)) || !keaRestarted(t0, t0.Add(-2*time.Minute)) {
		t.Fatal("a restart was not detected")
	}
	if keaRestarted(time.Time{}, t0) || keaRestarted(t0, time.Time{}) {
		t.Fatal("an unknown start time was read as a restart")
	}
}

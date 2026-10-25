package main

// An empty DHCP lease table is the one answer that means "Kea has lost its leases" as easily as "nobody holds an address". Reading it as the second
// ended every session at once. These tests pin that an untrusted empty answer is UNKNOWN (access is not renewed, no session is ended, nothing about
// the Entitlement or quota is touched) while a trusted one, and a table that names other holders, keep the established behaviour.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
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

type stabilityLeases struct {
	rows    []KeaLease
	err     error
	trusted bool
	asked   int
}

func (s *stabilityLeases) Leases() ([]KeaLease, error) { return s.rows, s.err }
func (s *stabilityLeases) EmptyAnswerTrusted(net.IP) (bool, string) {
	s.asked++
	return s.trusted, "Kea started 3 s ago (less than 300 s)"
}

func TestEmptyLeaseTableFromAnUntrustedKeaEndsNothing(t *testing.T) {
	src := &stabilityLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}}
	g, enf, p := provenWriter(src)
	s := sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")

	if res, err := p.submit(context.Background(), envelope(1, s), time.Now()); err != nil || res.Shaped != 1 {
		t.Fatalf("setup: %+v %v", res, err)
	}
	before := g.leaseOf("br-guest", "10.0.0.5")

	// Kea restarts onto an empty lease file: it answers "no leases" and has only just started.
	src.rows, src.trusted = nil, false
	g.advance(30 * time.Second)
	res, err := p.submit(context.Background(), envelope(2, s), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(enf.ended) != 0 {
		t.Fatalf("a Kea that has only just started ended %d session(s): %v", len(enf.ended), enf.ended)
	}
	if !g.isAuthorized("br-guest", "10.0.0.5") {
		t.Fatal("the guest was cut off by an empty table the appliance does not yet believe")
	}
	if after := g.leaseOf("br-guest", "10.0.0.5"); after >= before {
		t.Fatalf("access was RENEWED on unverified evidence (%s -> %s)", before, after)
	}
	if res.Shaped != 0 || p.unverifiedCount() != 1 {
		t.Fatalf("the unverified session is not visible: shaped=%d unverified=%d", res.Shaped, p.unverifiedCount())
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

// The lease returning (the client renewed into the restarted Kea) resumes normal renewal with no re-authentication.
func TestOwnershipReturningAfterAnUntrustedEmptyTableResumesRenewal(t *testing.T) {
	src := &stabilityLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}}
	g, enf, p := provenWriter(src)
	s := sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")
	if _, err := p.submit(context.Background(), envelope(1, s), time.Now()); err != nil {
		t.Fatal(err)
	}
	src.rows = nil
	g.advance(30 * time.Second)
	if _, err := p.submit(context.Background(), envelope(2, s), time.Now()); err != nil {
		t.Fatal(err)
	}
	if p.unverifiedCount() != 1 {
		t.Fatal("setup: the session should be unverified")
	}

	src.rows = []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}
	g.advance(10 * time.Second)
	res, err := p.submit(context.Background(), envelope(3, s), time.Now())
	if err != nil || res.Shaped != 1 {
		t.Fatalf("renewal did not resume: %+v %v", res, err)
	}
	if p.unverifiedCount() != 0 {
		t.Fatalf("the session is still reported unverified")
	}
	if len(enf.ended) != 0 {
		t.Fatalf("a session was ended along the way: %v", enf.ended)
	}
}

// Once Kea is stable and serves the subnet, an empty table is believed: the address has been given up.
func TestEmptyLeaseTableFromAStableKeaStillWithdrawsTheAddress(t *testing.T) {
	src := &stabilityLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}}
	g, enf, p := provenWriter(src)
	s := sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")
	if _, err := p.submit(context.Background(), envelope(1, s), time.Now()); err != nil {
		t.Fatal(err)
	}
	src.rows, src.trusted = nil, true
	g.advance(30 * time.Second)
	if _, err := p.submit(context.Background(), envelope(2, s), time.Now()); err != nil {
		t.Fatal(err)
	}
	if g.isAuthorized("br-guest", "10.0.0.5") {
		t.Fatal("a trusted empty table left the address authorized")
	}
	if len(enf.ended) != 1 {
		t.Fatalf("the session was not ended: %v", enf.ended)
	}
}

// A table that names OTHER holders is real evidence that this address was given up; it is not second-guessed.
func TestTableNamingOtherHoldersIsStillBelievedFromAnUnsettledKea(t *testing.T) {
	src := &stabilityLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}}
	g, enf, p := provenWriter(src)
	s := sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")
	if _, err := p.submit(context.Background(), envelope(1, s), time.Now()); err != nil {
		t.Fatal(err)
	}
	src.rows, src.trusted = []KeaLease{lease("10.0.0.9", "aa:bb:cc:dd:ee:ff")}, false
	g.advance(30 * time.Second)
	if _, err := p.submit(context.Background(), envelope(2, s), time.Now()); err != nil {
		t.Fatal(err)
	}
	if src.asked != 0 {
		t.Fatalf("the stability probe was consulted for a non-empty table (%d times)", src.asked)
	}
	if len(enf.ended) != 1 {
		t.Fatalf("an address missing from a populated table was not withdrawn: %v", enf.ended)
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
			got, why := k.EmptyAnswerTrusted(ip)
			if got != tc.trusted {
				t.Fatalf("trusted=%v (%s), want %v", got, why, tc.trusted)
			}
			if !got && why == "" {
				t.Fatal("an untrusted answer gave no reason")
			}
		})
	}
	// An unreachable control socket is never trusted either.
	if got, _ := newKeaClient(filepath.Join(t.TempDir(), "absent.sock")).EmptyAnswerTrusted(ip); got {
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

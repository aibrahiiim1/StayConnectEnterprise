package pmsd

// THE FINANCIAL RELAY IS TRANSPORT (decision D45). These tests pin what pmsd may and may not do with a posting
// command: it carries the exact authorised bytes on a steady link and returns the PA whose P# matches,
// verbatim; it answers NOT_TRANSMITTED only when it provably wrote nothing; and every other doubt -- a
// duplicate P#, an incomplete write, a timeout, a lost link -- is UNKNOWN, so nothing upstream can mistake a
// possibly-posted charge for an unsent one and send it again.

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/pms"
	"github.com/stayconnect/enterprise/data-plane/internal/postinghandoff"
)

const relayIface = "11111111-2222-3333-4444-555555555555"

func psBody(pn string) string {
	return "PS|RN1421|G#5|TA1500|PT|SOOG|CTWIFI|P#" + pn + "|WSOG|"
}

func cmd(pn int64, body string) postinghandoff.Request {
	return postinghandoff.Request{Version: postinghandoff.ProtocolVersion, InterfaceID: relayIface, PNumber: pn,
		Body: body, BodySHA256: postinghandoff.BodyHash(body), WaitMillis: 300}
}

type fakeLink struct {
	mu      sync.Mutex
	written []string
	enqueue bool
	werr    error
	after   func(body string) // runs after a successful write (e.g. the PMS answering)
}

func (f *fakeLink) submit(_ context.Context, body string) (bool, error) {
	f.mu.Lock()
	if !f.enqueue {
		f.mu.Unlock()
		return false, errors.New("writer stopped")
	}
	f.written = append(f.written, body)
	f.mu.Unlock()
	if f.werr != nil {
		return true, f.werr
	}
	if f.after != nil {
		go f.after(body)
	}
	return true, nil
}

func (f *fakeLink) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.written) }

func authorised(context.Context, string, int64, string) (string, error) { return "AUTHORISED", nil }

func newRelayWithLink(t *testing.T, auth Authoriser, link *fakeLink) (*FinancialRelay, *relayPort) {
	t.Helper()
	r := NewFinancialRelay(true, auth, nil)
	p := r.attach(relayIface, link.submit)
	p.setSteady(true)
	return r, p
}

func TestRelay_CarriesExactBytesAndReturnsTheMatchingPAVerbatim(t *testing.T) {
	link := &fakeLink{enqueue: true}
	r, p := newRelayWithLink(t, authorised, link)
	body := psBody("42")
	link.after = func(string) {
		time.Sleep(10 * time.Millisecond)
		p.onPA("PA|P#41|ASOK|")        // someone else's answer: never matched to this command
		p.onPA("PA|RN1421|P#42|ASOK|") // this command's answer
	}
	resp := r.Handle(context.Background(), cmd(42, body))
	if resp.Result != postinghandoff.Answered || resp.PABody != "PA|RN1421|P#42|ASOK|" {
		t.Fatalf("want the matching PA verbatim, got %+v", resp)
	}
	if link.count() != 1 || link.written[0] != body {
		t.Fatalf("pmsd must write exactly the authorised bytes once: %q", link.written)
	}
}

func TestRelay_RefusalsBeforeAnyWriteAreNotTransmitted(t *testing.T) {
	body := psBody("7")
	tampered := cmd(7, body)
	tampered.Body = strings.Replace(body, "TA1500", "TA9999", 1) // hash no longer matches
	wrongPN := cmd(8, body)                                      // body carries P#7
	notPS := cmd(7, "PA|P#7|ASOK|")
	notPS.BodySHA256 = postinghandoff.BodyHash(notPS.Body)
	for name, tc := range map[string]struct {
		enabled bool
		steady  bool
		attach  bool
		auth    Authoriser
		req     postinghandoff.Request
		code    string
	}{
		"not deployed on pmsd":    {false, true, true, authorised, cmd(7, body), "PMS_TRANSMIT_NOT_DEPLOYED_ON_PMSD"},
		"tampered bytes":          {true, true, true, authorised, tampered, "COMMAND_MALFORMED"},
		"body carries another P#": {true, true, true, authorised, wrongPN, "COMMAND_MALFORMED"},
		"not a PS":                {true, true, true, authorised, notPS, "COMMAND_MALFORMED"},
		"no link":                 {true, true, false, authorised, cmd(7, body), "LINK_NOT_CONNECTED"},
		"resyncing":               {true, false, true, authorised, cmd(7, body), "LINK_RESYNCING"},
		"not authorised": {true, true, true, func(context.Context, string, int64, string) (string, error) {
			return "COMMAND_BYTES_NOT_AUTHORISED", nil
		}, cmd(7, body), "COMMAND_BYTES_NOT_AUTHORISED"},
		"authoriser down": {true, true, true, func(context.Context, string, int64, string) (string, error) {
			return "", errors.New("db down")
		}, cmd(7, body), "AUTHORISATION_UNAVAILABLE"},
	} {
		t.Run(name, func(t *testing.T) {
			link := &fakeLink{enqueue: true}
			r := NewFinancialRelay(tc.enabled, tc.auth, nil)
			if tc.attach {
				p := r.attach(relayIface, link.submit)
				p.setSteady(tc.steady)
			}
			resp := r.Handle(context.Background(), tc.req)
			if resp.Result != postinghandoff.NotTransmitted || resp.Code != tc.code {
				t.Fatalf("want NOT_TRANSMITTED %s, got %+v", tc.code, resp)
			}
			if link.count() != 0 {
				t.Fatal("a refused command must write nothing")
			}
		})
	}
}

func TestRelay_DuplicateCommandIsNeverNotTransmitted(t *testing.T) {
	link := &fakeLink{enqueue: true}
	r, _ := newRelayWithLink(t, authorised, link)
	first := r.Handle(context.Background(), cmd(9, psBody("9"))) // no PA: times out
	if first.Result != postinghandoff.Unknown || first.Code != "ANSWER_TIMEOUT" {
		t.Fatalf("an unanswered command is UNKNOWN, got %+v", first)
	}
	again := r.Handle(context.Background(), cmd(9, psBody("9")))
	if again.Result != postinghandoff.Unknown || again.Code != "DUPLICATE_COMMAND" {
		t.Fatalf("a P# already carried must answer UNKNOWN, never not-sent: %+v", again)
	}
	if link.count() != 1 {
		t.Fatalf("the same P# is written at most once, wrote %d", link.count())
	}
}

func TestRelay_AmbiguousWritesAreUnknown(t *testing.T) {
	t.Run("incomplete write", func(t *testing.T) {
		link := &fakeLink{enqueue: true, werr: errors.New("broken pipe")}
		r, _ := newRelayWithLink(t, authorised, link)
		if resp := r.Handle(context.Background(), cmd(3, psBody("3"))); resp.Result != postinghandoff.Unknown {
			t.Fatalf("a write that was taken and failed may have reached the PMS: %+v", resp)
		}
	})
	t.Run("link lost after write", func(t *testing.T) {
		link := &fakeLink{enqueue: true}
		r, p := newRelayWithLink(t, authorised, link)
		link.after = func(string) { time.Sleep(10 * time.Millisecond); r.detach(p) }
		if resp := r.Handle(context.Background(), cmd(4, psBody("4"))); resp.Result != postinghandoff.Unknown || resp.Code != "LINK_LOST_AFTER_WRITE" {
			t.Fatalf("got %+v", resp)
		}
	})
	t.Run("writer never took it", func(t *testing.T) {
		link := &fakeLink{enqueue: false}
		r, _ := newRelayWithLink(t, authorised, link)
		if resp := r.Handle(context.Background(), cmd(5, psBody("5"))); resp.Result != postinghandoff.NotTransmitted {
			t.Fatalf("a command the writer never took was provably not sent: %+v", resp)
		}
		if r.wasCarried(relayIface, 5) {
			t.Fatal("a P# that was never written is not recorded as carried")
		}
	})
}

func TestRelay_OneCommandPerLink(t *testing.T) {
	link := &fakeLink{enqueue: true}
	r, _ := newRelayWithLink(t, authorised, link)
	done := make(chan postinghandoff.Response, 1)
	go func() { done <- r.Handle(context.Background(), cmd(11, psBody("11"))) }()
	time.Sleep(50 * time.Millisecond)
	if resp := r.Handle(context.Background(), cmd(12, psBody("12"))); resp.Result != postinghandoff.NotTransmitted || resp.Code != "LINK_BUSY" {
		t.Fatalf("a second command while one is outstanding is refused unsent: %+v", resp)
	}
	<-done
}

func TestRelayTransmitDeployedNeedsAllThreeFlags(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	all := map[string]string{envPhase4Master: "1", envPhase4Outbox: "true", envPhase4Transmit: "1"}
	if !RelayTransmitDeployed(env(all)) {
		t.Fatal("all three on deploys the relay")
	}
	for _, k := range []string{envPhase4Master, envPhase4Outbox, envPhase4Transmit} {
		m := map[string]string{}
		for kk, v := range all {
			m[kk] = v
		}
		m[k] = "0"
		if RelayTransmitDeployed(env(m)) {
			t.Fatalf("%s off must not deploy the relay", k)
		}
		m[k] = "maybe"
		if RelayTransmitDeployed(env(m)) {
			t.Fatalf("%s malformed must not deploy the relay", k)
		}
	}
}

// The single writer still refuses PS for every ordinary caller; only the financial path writes one, and that
// path writes nothing but PS.
func TestFinancialFrameIsPSOnlyAndOrdinaryFramesStillRefusePS(t *testing.T) {
	client, server := pipePair(t)
	g := &guardedConn{c: client, writeTimeout: time.Second}
	if err := g.writeFrame(psBody("1")); err == nil {
		t.Fatal("the read-only writer must still refuse a PS")
	}
	for _, bad := range []string{"PA|P#1|ASOK|", "LS|", "GI|RN1|G#1|"} {
		if err := g.writeFinancialFrame(bad); err == nil {
			t.Fatalf("the financial path must refuse %q", bad[:2])
		}
	}
	go func() { _ = g.writeFinancialFrame(psBody("1")) }()
	got, err := pms.ReadFramedRecord(bufio.NewReader(server))
	if err != nil || got != psBody("1") {
		t.Fatalf("the PS reaches the wire verbatim: %q %v", got, err)
	}
}

func pipePair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	return a, b
}

// THE WHOLE LINK: the real FIAS adapter, its single writer and read loop, and a fake PMS. The PS is carried
// only once the initial DS..DE generation is published, reaches the PMS byte for byte on the SAME connection
// the roster arrives on, and the PMS's PA comes back to the command verbatim. The feed keeps working.
func TestAdapter_CarriesAnAuthorisedPSOnItsOwnLinkAndReturnsThePA(t *testing.T) {
	client, server := net.Pipe()
	relay := NewFinancialRelay(true, authorised, nil)
	dial := NewFIASDialWithRelay(func(context.Context, string, string) (net.Conn, error) { return client, nil },
		testKeys(), time.Now, nil, relay)
	conn, err := dial(context.Background(), DialParams{Iface: iface("i1"), Rev: testRev()})
	if err != nil {
		t.Fatal(err)
	}
	ifaceID := iface("i1").ID
	body := "PS|RN1408|G#12345|TA1500|PT|SOOG|CTWIFI|P#77|WSOG|"

	var sawPS string
	var mu sync.Mutex
	peer := make(chan struct{})
	go func() {
		defer close(peer)
		br := bufio.NewReader(server)
		for i := 0; i < 6; i++ { // LS, LD, LR x3, DR
			if _, err := pms.ReadFramedRecord(br); err != nil {
				return
			}
		}
		for _, rec := range []string{"DS|", "DE|", "LA|"} {
			_ = pms.WriteFramedRecord(server, rec)
		}
		for {
			got, err := pms.ReadFramedRecord(br)
			if err != nil {
				return
			}
			if pms.RecordID(got) == "PS" {
				mu.Lock()
				sawPS = got
				mu.Unlock()
				_ = pms.WriteFramedRecord(server, "PA|RN1408|P#77|ASOK|DA260101|TI120000|")
				_ = pms.WriteFramedRecord(server, "GI|RN1409|G#12346|GNDoe|GA260101|GD260105|")
				time.Sleep(50 * time.Millisecond)
				_ = server.Close()
				return
			}
		}
	}()
	sink := &recordingSink{q: NewBoundedQueue(16, time.Second)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	served := make(chan struct{})
	go func() { _ = conn.(*fiasAdapter).Serve(ctx, sink); close(served) }()

	deadline := time.Now().Add(3 * time.Second)
	for {
		if p := relay.portFor(ifaceID); p != nil && p.steady.Load() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the link never became steady after the initial resync")
		}
		time.Sleep(5 * time.Millisecond)
	}
	req := postinghandoff.Request{Version: 1, InterfaceID: ifaceID, PNumber: 77, Body: body,
		BodySHA256: postinghandoff.BodyHash(body), WaitMillis: 2000}
	resp := relay.Handle(ctx, req)
	<-peer
	<-served
	if resp.Result != postinghandoff.Answered || resp.PABody != "PA|RN1408|P#77|ASOK|DA260101|TI120000|" {
		t.Fatalf("want the PA verbatim, got %+v", resp)
	}
	mu.Lock()
	defer mu.Unlock()
	if sawPS != body {
		t.Fatalf("the PMS must receive exactly the authorised bytes: %q", sawPS)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.events) != 1 || sink.continuityFlt != 0 {
		t.Fatalf("the feed keeps flowing and a PA is not a fault: events=%d faults=%d", len(sink.events), sink.continuityFlt)
	}
}

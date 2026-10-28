package conntrack

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"testing"
)

func attr(typ uint16, data []byte) []byte {
	b := make([]byte, nlaHdrLen, align4(nlaHdrLen+len(data)))
	binary.LittleEndian.PutUint16(b[0:2], uint16(nlaHdrLen+len(data)))
	binary.LittleEndian.PutUint16(b[2:4], typ)
	b = append(b, data...)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

func nested(typ uint16, children ...[]byte) []byte {
	return attr(typ|nlaFNested, bytes.Join(children, nil))
}

func tuple(typ uint16, src, dst string, proto byte, sport, dport uint16) []byte {
	ipb := nested(ctaTupleIP, attr(ctaIPv4Src, net.ParseIP(src).To4()), attr(ctaIPv4Dst, net.ParseIP(dst).To4()))
	ports := make([]byte, 2)
	binary.BigEndian.PutUint16(ports, sport)
	dp := make([]byte, 2)
	binary.BigEndian.PutUint16(dp, dport)
	protob := nested(2, attr(1, []byte{proto}), attr(2, ports), attr(3, dp))
	return nested(typ, ipb, protob)
}

// a conntrack entry as the kernel would send it in a dump.
func ctMsg(seq uint32, origSrc, origDst, replySrc, replyDst string, zone bool) []byte {
	payload := nfgen()
	payload = append(payload, tuple(ctaTupleOrig, origSrc, origDst, 6, 40000, 443)...)
	payload = append(payload, tuple(ctaTupleReply, replySrc, replyDst, 6, 443, 40000)...)
	if zone {
		payload = append(payload, attr(ctaZone, []byte{0, 7, 0, 0})...)
	}
	return putNlmsg(nfnlSubsysCTNetlink<<8|ipctnlMsgCtNew, nlmFMulti, seq, payload)
}

func doneMsg(seq uint32) []byte { return putNlmsg(nlmsgDone, nlmFMulti, seq, make([]byte, 4)) }

func errMsg(seq uint32, code int32) []byte {
	p := make([]byte, 4+nlmsgHdrLen)
	binary.LittleEndian.PutUint32(p[0:4], uint32(code))
	return putNlmsg(nlmsgError, 0, seq, p)
}

func TestDumpParseSelectsOnlyTheGuestsFlows(t *testing.T) {
	var dgram []byte
	dgram = append(dgram, ctMsg(1, "10.20.0.50", "93.184.216.34", "93.184.216.34", "192.168.60.25", false)...)
	dgram = append(dgram, ctMsg(1, "10.20.0.51", "93.184.216.34", "93.184.216.34", "192.168.60.25", true)...)
	dgram = append(dgram, ctMsg(1, "10.20.0.50", "1.2.3.4", "10.20.0.1", "10.20.0.50", false)...)
	dgram = append(dgram, doneMsg(1)...)

	r := parseMessages(dgram)
	if r.err != nil || !r.done {
		t.Fatalf("parse: err=%v done=%v", r.err, r.done)
	}
	if len(r.entries) != 3 {
		t.Fatalf("parsed %d entries, want 3", len(r.entries))
	}
	got := selectEntries(r.entries, []net.IP{net.ParseIP("10.20.0.50")})
	if len(got) != 2 {
		t.Fatalf("selected %d entries for the guest, want its 2 flows", len(got))
	}
	for _, e := range got {
		if e.OrigSrc.String() != "10.20.0.50" {
			t.Fatalf("selected another guest's flow: %+v", e)
		}
	}
	// The WAN address is shared by every guest's masqueraded flow; it must never be a selector by accident.
	if n := len(selectEntries(r.entries, []net.IP{net.ParseIP("10.99.0.1")})); n != 0 {
		t.Fatalf("an unrelated address selected %d entries", n)
	}
}

func TestDeleteRequestCarriesTheOriginalTupleAndZone(t *testing.T) {
	r := parseMessages(ctMsg(1, "10.20.0.51", "8.8.8.8", "8.8.8.8", "192.168.60.25", true))
	if len(r.entries) != 1 {
		t.Fatalf("parse: %+v", r)
	}
	e := r.entries[0]
	req := buildDeleteRequest(e, 99)
	if got := binary.LittleEndian.Uint16(req[4:6]); got != nfnlSubsysCTNetlink<<8|ipctnlMsgCtDelete {
		t.Fatalf("message type %#x is not IPCTNL_MSG_CT_DELETE", got)
	}
	if flags := binary.LittleEndian.Uint16(req[6:8]); flags&nlmFAck == 0 || flags&nlmFDump != 0 {
		t.Fatalf("delete flags %#x: must ask for an ACK and must not be a dump (a dump-delete flushes everything)", flags)
	}
	if int(binary.LittleEndian.Uint32(req[0:4])) != len(req) {
		t.Fatal("nlmsg length does not match the message")
	}
	body := req[nlmsgHdrLen+nfgenmsgLen:]
	if !bytes.HasPrefix(body, e.rawOrig) {
		t.Fatal("the delete does not carry the dumped original tuple")
	}
	if !bytes.Contains(body, e.rawZone) {
		t.Fatal("the delete drops the entry's zone, so it would name a different entry")
	}
	attrs, err := parseAttrs(body)
	if err != nil || len(attrs) != 2 || attrs[0].typ != ctaTupleOrig || attrs[1].typ != ctaZone {
		t.Fatalf("delete attributes: %+v err=%v", attrs, err)
	}
	// An empty tuple would turn the delete into a flush of the whole table.
	if _, ok := parseEntry(nfgen()); ok {
		t.Fatal("an entry with no original tuple was accepted")
	}
}

func TestDumpRequestIsAnIPv4Dump(t *testing.T) {
	req := buildDumpRequest(7)
	if binary.LittleEndian.Uint16(req[4:6]) != nfnlSubsysCTNetlink<<8|ipctnlMsgCtGet {
		t.Fatal("not IPCTNL_MSG_CT_GET")
	}
	if binary.LittleEndian.Uint16(req[6:8])&nlmFDump != nlmFDump {
		t.Fatal("not a dump")
	}
	if req[nlmsgHdrLen] != afINET {
		t.Fatal("not the IPv4 table")
	}
}

func TestErrorsAndAcks(t *testing.T) {
	if r := parseMessages(errMsg(1, 0)); r.err != nil {
		t.Fatalf("an ACK is not an error: %v", r.err)
	}
	if r := parseMessages(errMsg(1, -2)); !errors.Is(r.err, enoent) {
		t.Fatalf("ENOENT not recognised: %v", r.err)
	}
	if r := parseMessages([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}); r.err == nil {
		t.Fatal("a malformed message was accepted")
	}
}

func TestToolSummaryParsing(t *testing.T) {
	for out, want := range map[string]int{
		"conntrack v1.4.6 (conntrack-tools): 3 flow entries have been deleted.\n": 3,
		"conntrack v1.4.6 (conntrack-tools): 0 flow entries have been deleted.":   0,
	} {
		n, ok := parseToolDeleted(out)
		if !ok || n != want {
			t.Fatalf("%q: got %d ok=%v", out, n, ok)
		}
	}
	if _, ok := parseToolDeleted("conntrack: command not found"); ok {
		t.Fatal("garbage parsed as a summary")
	}
}

func TestFallbackToToolAndNothingMatchedIsSuccess(t *testing.T) {
	var calls []string
	s := &System{
		netlink: func(context.Context, []net.IP) (int, error) { return 0, errors.New("EPERM") },
		exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			if args[3] == "-s" {
				return []byte("conntrack v1.4.6 (conntrack-tools): 2 flow entries have been deleted.\n"), nil
			}
			return []byte("conntrack v1.4.6 (conntrack-tools): 0 flow entries have been deleted.\n"), errors.New("exit status 1")
		},
	}
	n, err := s.FlushAddrs(context.Background(), []net.IP{net.ParseIP("10.20.0.50"), net.ParseIP("10.20.0.50"), net.ParseIP("::1")})
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if len(calls) != 3 {
		t.Fatalf("the duplicate address or the IPv6 one was flushed too: %v", calls)
	}
}

func TestBothFailingIsReported(t *testing.T) {
	s := &System{
		netlink: func(context.Context, []net.IP) (int, error) { return 0, errors.New("EPERM") },
		exec: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("conntrack: command not found"), errors.New("exec: not found")
		},
	}
	if _, err := s.FlushAddrs(context.Background(), []net.IP{net.ParseIP("10.20.0.50")}); err == nil ||
		!strings.Contains(err.Error(), "netlink") || !strings.Contains(err.Error(), "tool") {
		t.Fatalf("a flush that reached nothing must say so with both causes: %v", err)
	}
	if n, err := s.FlushAddrs(context.Background(), nil); n != 0 || err != nil {
		t.Fatalf("an empty flush is a no-op: %d %v", n, err)
	}
}

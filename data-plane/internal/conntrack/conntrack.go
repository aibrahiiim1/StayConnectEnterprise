// Package conntrack ends the kernel's tracked flows for a set of IPv4 addresses.
//
// The guest forward chain accepts `ct state established,related` before the authorization sets
// (internal/netcfg/render_nft.go), so removing an address from the set stops only new connections. Every
// teardown must flush the address's conntrack entries so the old flow's next packet is NEW and dropped.
//
// ctnetlink is used directly; the `conntrack` tool is the fallback.
package conntrack

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
)

type Flusher interface {
	FlushAddrs(ctx context.Context, ips []net.IP) (int, error)
}

// System tries netlink first, then the conntrack tool.
type System struct {
	// ToolPath defaults to "conntrack" on PATH.
	ToolPath string
	// netlink and exec are test seams.
	netlink func(ctx context.Context, ips []net.IP) (int, error)
	exec    func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func New() *System { return &System{} }

// FlushAddrs deletes every IPv4 entry whose original or reply tuple names one of ips. IPv6 is ignored: none
// crosses a client network.
func (s *System) FlushAddrs(ctx context.Context, ips []net.IP) (int, error) {
	v4 := uniqueV4(ips)
	if len(v4) == 0 {
		return 0, nil
	}
	nl := s.netlink
	if nl == nil {
		nl = netlinkFlush
	}
	n, err := nl(ctx, v4)
	if err == nil {
		return n, nil
	}
	// netlink can be refused by a sandbox or unusual kernel; report both causes if the tool also fails.
	m, terr := s.toolFlush(ctx, v4)
	if terr == nil {
		return m, nil
	}
	return n + m, fmt.Errorf("conntrack flush failed: netlink: %v; tool: %v", err, terr)
}

func (s *System) toolFlush(ctx context.Context, ips []net.IP) (int, error) {
	run := s.exec
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		}
	}
	tool := s.ToolPath
	if tool == "" {
		tool = "conntrack"
	}
	total := 0
	var errs []string
	for _, ip := range ips {
		// --reply-dst covers a flow whose reply is addressed to the guest after DNAT.
		for _, sel := range [][]string{{"-s"}, {"-d"}, {"--reply-dst"}} {
			args := append([]string{"-D", "-f", "ipv4"}, append(sel, ip.String())...)
			out, err := run(ctx, tool, args...)
			n, ok := parseToolDeleted(string(out))
			if err != nil && !ok {
				// The tool exits 1 with "0 flow entries have been deleted" when nothing matched; not a failure.
				errs = append(errs, fmt.Sprintf("%s %s: %v %s", tool, strings.Join(args, " "), err, strings.TrimSpace(string(out))))
				continue
			}
			total += n
		}
	}
	if len(errs) > 0 {
		return total, errors.New(strings.Join(errs, "; "))
	}
	return total, nil
}

// parseToolDeleted reads e.g. "conntrack v1.4.6 (conntrack-tools): 3 flow entries have been deleted."
func parseToolDeleted(out string) (int, bool) {
	const marker = " flow entries have been deleted"
	for _, line := range strings.Split(out, "\n") {
		i := strings.Index(line, marker)
		if i < 0 {
			continue
		}
		fields := strings.Fields(line[:i])
		if len(fields) == 0 {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(fields[len(fields)-1], "%d", &n); err == nil && n >= 0 {
			return n, true
		}
	}
	return 0, false
}

func uniqueV4(ips []net.IP) []net.IP {
	seen := map[string]bool{}
	out := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		v4 := ip.To4()
		if v4 == nil || v4.IsUnspecified() {
			continue
		}
		k := v4.String()
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, v4)
	}
	return out
}

const (
	nfnlSubsysCTNetlink = 1
	ipctnlMsgCtNew      = 0
	ipctnlMsgCtGet      = 1
	ipctnlMsgCtDelete   = 2

	nlmFRequest = 0x1
	nlmFMulti   = 0x2
	nlmFAck     = 0x4
	nlmFDump    = 0x300

	nlmsgError = 0x2
	nlmsgDone  = 0x3

	ctaTupleOrig  = 1
	ctaTupleReply = 2
	ctaZone       = 18
	ctaTupleIP    = 1
	ctaIPv4Src    = 1
	ctaIPv4Dst    = 2

	nlaFNested  = 0x8000
	nlaTypeMask = ^uint16(nlaFNested | 0x4000)
	afINET      = 2
	nlmsgHdrLen = 16
	nfgenmsgLen = 4
	nlaHdrLen   = 4
	nfnetlinkV0 = 0
)

// entry carries the four addresses that decide ownership and the raw attributes needed to delete exactly it.
type entry struct {
	OrigSrc, OrigDst, ReplySrc, ReplyDst net.IP
	rawOrig                              []byte // the complete CTA_TUPLE_ORIG attribute, header included
	rawZone                              []byte // the complete CTA_ZONE attribute, if the entry has one
}

func (e entry) matches(set map[string]bool) bool {
	for _, ip := range []net.IP{e.OrigSrc, e.OrigDst, e.ReplySrc, e.ReplyDst} {
		if ip != nil && set[ip.String()] {
			return true
		}
	}
	return false
}

func align4(n int) int { return (n + 3) &^ 3 }

func putNlmsg(msgType, flags uint16, seq uint32, payload []byte) []byte {
	b := make([]byte, nlmsgHdrLen, nlmsgHdrLen+len(payload))
	binary.LittleEndian.PutUint32(b[0:4], uint32(nlmsgHdrLen+len(payload)))
	binary.LittleEndian.PutUint16(b[4:6], msgType)
	binary.LittleEndian.PutUint16(b[6:8], flags)
	binary.LittleEndian.PutUint32(b[8:12], seq)
	binary.LittleEndian.PutUint32(b[12:16], 0)
	return append(b, payload...)
}

func nfgen() []byte { return []byte{afINET, nfnetlinkV0, 0, 0} }

func buildDumpRequest(seq uint32) []byte {
	return putNlmsg(nfnlSubsysCTNetlink<<8|ipctnlMsgCtGet, nlmFRequest|nlmFDump, seq, nfgen())
}

// buildDeleteRequest names exactly one entry by its original tuple (and zone).
func buildDeleteRequest(e entry, seq uint32) []byte {
	payload := nfgen()
	payload = append(payload, padded(e.rawOrig)...)
	if len(e.rawZone) > 0 {
		payload = append(payload, padded(e.rawZone)...)
	}
	return putNlmsg(nfnlSubsysCTNetlink<<8|ipctnlMsgCtDelete, nlmFRequest|nlmFAck, seq, payload)
}

func padded(attr []byte) []byte {
	out := make([]byte, align4(len(attr)))
	copy(out, attr)
	return out
}

type nlAttr struct {
	typ  uint16
	data []byte
	raw  []byte
}

func parseAttrs(b []byte) ([]nlAttr, error) {
	var out []nlAttr
	for len(b) >= nlaHdrLen {
		l := int(binary.LittleEndian.Uint16(b[0:2]))
		t := binary.LittleEndian.Uint16(b[2:4])
		if l < nlaHdrLen || l > len(b) {
			return out, fmt.Errorf("malformed netlink attribute (len %d of %d)", l, len(b))
		}
		out = append(out, nlAttr{typ: t & nlaTypeMask, data: b[nlaHdrLen:l], raw: b[:l]})
		n := align4(l)
		if n > len(b) {
			n = len(b)
		}
		b = b[n:]
	}
	return out, nil
}

func tupleAddrs(tuple []byte) (src, dst net.IP) {
	attrs, _ := parseAttrs(tuple)
	for _, a := range attrs {
		if a.typ != ctaTupleIP {
			continue
		}
		ipAttrs, _ := parseAttrs(a.data)
		for _, ia := range ipAttrs {
			if len(ia.data) != 4 {
				continue
			}
			ip := net.IPv4(ia.data[0], ia.data[1], ia.data[2], ia.data[3]).To4()
			switch ia.typ {
			case ctaIPv4Src:
				src = ip
			case ctaIPv4Dst:
				dst = ip
			}
		}
	}
	return src, dst
}

func parseEntry(payload []byte) (entry, bool) {
	if len(payload) < nfgenmsgLen {
		return entry{}, false
	}
	attrs, err := parseAttrs(payload[nfgenmsgLen:])
	if err != nil && len(attrs) == 0 {
		return entry{}, false
	}
	var e entry
	for _, a := range attrs {
		switch a.typ {
		case ctaTupleOrig:
			e.OrigSrc, e.OrigDst = tupleAddrs(a.data)
			e.rawOrig = append([]byte(nil), a.raw...)
		case ctaTupleReply:
			e.ReplySrc, e.ReplyDst = tupleAddrs(a.data)
		case ctaZone:
			e.rawZone = append([]byte(nil), a.raw...)
		}
	}
	return e, len(e.rawOrig) > 0
}

type dumpResult struct {
	entries []entry
	done    bool
	err     error
	// interrupted: a message carried NLM_F_DUMP_INTR, so the table changed during the dump and entries may be missing.
	interrupted bool
}

// nlmFDumpIntr is NLM_F_DUMP_INTR.
const nlmFDumpIntr = 0x10

// parseMessages reads every netlink message in one datagram. done reports NLMSG_DONE; an NLMSG_ERROR with a
// non-zero code is returned as err (a zero code is an ACK).
func parseMessages(buf []byte) dumpResult {
	var r dumpResult
	for len(buf) >= nlmsgHdrLen {
		l := int(binary.LittleEndian.Uint32(buf[0:4]))
		t := binary.LittleEndian.Uint16(buf[4:6])
		if l < nlmsgHdrLen || l > len(buf) {
			r.err = fmt.Errorf("malformed netlink message (len %d of %d)", l, len(buf))
			return r
		}
		payload := buf[nlmsgHdrLen:l]
		if binary.LittleEndian.Uint16(buf[6:8])&nlmFDumpIntr != 0 {
			r.interrupted = true
		}
		switch {
		case t == nlmsgDone:
			r.done = true
		case t == nlmsgError:
			if len(payload) >= 4 {
				if code := int32(binary.LittleEndian.Uint32(payload[0:4])); code != 0 {
					r.err = errnoError(-code)
				}
			}
		case t == nfnlSubsysCTNetlink<<8|ipctnlMsgCtNew:
			if e, ok := parseEntry(payload); ok {
				r.entries = append(r.entries, e)
			}
		}
		n := align4(l)
		if n > len(buf) {
			n = len(buf)
		}
		buf = buf[n:]
	}
	return r
}

type errnoError int32

func (e errnoError) Error() string { return fmt.Sprintf("netlink errno %d", int32(e)) }

// enoent: the entry was already gone (expired or flushed elsewhere); not a failure.
const enoent = errnoError(2)

func selectEntries(all []entry, ips []net.IP) []entry {
	set := map[string]bool{}
	for _, ip := range ips {
		set[ip.To4().String()] = true
	}
	var out []entry
	for _, e := range all {
		if e.matches(set) {
			out = append(out, e)
		}
	}
	return out
}

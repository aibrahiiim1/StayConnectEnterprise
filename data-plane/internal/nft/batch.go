package nft

// Batched element writes for the Phase-3 authorization sets.
//
// The per-element calls (LeaseIn, LeaseMAC, DenyIn, DenyMAC, AuthorizedIn) each begin by listing the WHOLE set, and every listing is a forked
// `nft -j list set` whose cost grows with the number of guests: a pass over N sessions therefore costs O(N^2). The calls here read each set once
// per batch, write every element of the batch in ONE `nft -f -` transaction, and verify the result with one more read. Attribution is kept: if
// the transaction is refused (one element cannot be written, or an element lapsed between the read and the write) the batch is retried against a
// fresh read, and then executed element by element with the per-element calls, so the failing element - and only it - carries the error.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// BatchChunk bounds the elements of one transaction, so one refused element costs at most one chunk's retry.
const BatchChunk = 256

// ErrStillPresent is reported for an element that a revocation batch did not remove.
var ErrStillPresent = errors.New("nft: the authorization is still present after revocation")

// LeaseReq is one authorization to assert. An empty MAC asserts the IP-only element alone.
type LeaseReq struct {
	Iface string
	IP    net.IP
	MAC   string
	TTL   time.Duration
}

// AddrReq names the authorization(s) of one (interface, address) to remove.
type AddrReq struct {
	Iface string
	IP    net.IP
}

func (c *Client) execInOrReal(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	if c.execIn != nil {
		return c.execIn(ctx, stdin, name, args...)
	}
	return realExecStdin(ctx, stdin, name, args...)
}

// runScript executes the script as ONE nft transaction: all of it applies or none of it does.
func (c *Client) runTxn(ctx context.Context, lines []string) error {
	script := strings.Join(lines, "\n") + "\n"
	out, err := c.execInOrReal(ctx, []byte(script), c.NftPath, "-f", "-")
	if err != nil {
		return fmt.Errorf("nft -f - (%d operations): %w — %s", len(lines), err, strings.TrimSpace(string(out)))
	}
	return nil
}

type setSnapshot struct {
	ip     map[string][]Element // by address text
	mac    map[string][]Element
	hasMAC bool
	// macErr: the MAC-bound set could not be read and the caller does not require it (binding is off), so MAC operations are skipped.
	macErr error
}

func (c *Client) snapshot(ctx context.Context, withMAC, requireMAC bool) (setSnapshot, error) {
	s := setSnapshot{ip: map[string][]Element{}, hasMAC: withMAC}
	els, err := c.ListIn(ctx, Phase3AuthV4)
	if err != nil {
		return s, err
	}
	for _, e := range els {
		s.ip[e.IP.String()] = append(s.ip[e.IP.String()], e)
	}
	if withMAC {
		s.mac = map[string][]Element{}
		els, err := c.ListIn(ctx, Phase3MACV4)
		if err != nil {
			if requireMAC {
				return s, err
			}
			// With MAC binding off the IP-only view stands and the MAC-bound set is best effort, as in the per-element calls.
			s.macErr = err
		}
		for _, e := range els {
			s.mac[e.IP.String()] = append(s.mac[e.IP.String()], e)
		}
	}
	return s, nil
}

// ipPresent mirrors AuthorizedIn's membership rule for the IP-only set.
func (c *Client) ipPresent(s setSnapshot, concat bool, iface string, ip4 net.IP) bool {
	for _, e := range s.ip[ip4.String()] {
		if !concat || iface == "" || e.Iface == "" || e.Iface == iface {
			return true
		}
	}
	return false
}

func leaseValid(r LeaseReq) (net.IP, int, string, error) {
	if r.TTL <= 0 {
		return nil, 0, "", ErrUnboundedLease
	}
	ip4 := r.IP.To4()
	if ip4 == nil {
		return nil, 0, "", fmt.Errorf("only IPv4 supported: %s", r.IP)
	}
	secs, ok := leaseSeconds(r.TTL)
	if !ok {
		return nil, 0, "", ErrLeaseTooShort
	}
	mac := ""
	if r.MAC != "" {
		mac = NormalizeMAC(r.MAC)
		if mac == "" {
			return nil, 0, "", fmt.Errorf("nft: a MAC-bound authorization needs a 6-octet hardware address")
		}
	}
	return ip4, secs, mac, nil
}

func reqKey(iface string, ip net.IP) string { return iface + "|" + ip.String() }

// LeaseBatch asserts every authorization of reqs (the per-element LeaseIn + LeaseMAC semantics: a refresh is a delete+add in one transaction, so
// nobody is unauthorized in between) and verifies they are present. The result has one entry per request; nil means authorized and verified. When
// requireMAC is false a MAC-set problem does not fail the request, exactly as LeaseMAC's failure is tolerated while MAC binding is off.
func (c *Client) LeaseBatch(ctx context.Context, reqs []LeaseReq, requireMAC bool) []error {
	errs := make([]error, len(reqs))
	for start := 0; start < len(reqs); start += BatchChunk {
		end := start + BatchChunk
		if end > len(reqs) {
			end = len(reqs)
		}
		c.leaseChunk(ctx, reqs[start:end], errs[start:end], requireMAC)
	}
	return errs
}

func (c *Client) leaseChunk(ctx context.Context, reqs []LeaseReq, errs []error, requireMAC bool) {
	type item struct {
		i    int
		ip4  net.IP
		secs int
		mac  string
	}
	var items []item
	last := map[string]int{} // a repeated (iface, ip) is applied once, with its last request
	for i, r := range reqs {
		ip4, secs, mac, err := leaseValid(r)
		if err != nil {
			errs[i] = err
			continue
		}
		last[reqKey(r.Iface, ip4)] = i
		items = append(items, item{i, ip4, secs, mac})
	}
	if len(items) == 0 {
		return
	}
	withMAC := false
	for _, it := range items {
		if it.mac != "" {
			withMAC = true
		}
	}
	var werr error
	for attempt := 0; attempt < 2; attempt++ {
		snap, err := c.snapshot(ctx, withMAC, requireMAC)
		if err != nil {
			werr = err
			break
		}
		concat := c.probeConcatIn(ctx, Phase3AuthV4)
		var lines []string
		for _, it := range items {
			r := reqs[it.i]
			if last[reqKey(r.Iface, it.ip4)] != it.i {
				continue
			}
			key := c.keyIn(ctx, Phase3AuthV4, r.Iface, it.ip4)
			if c.ipPresent(snap, concat, r.Iface, it.ip4) {
				lines = append(lines, fmt.Sprintf("delete element inet stayconnect %s { %s }", Phase3AuthV4, key))
			}
			lines = append(lines, fmt.Sprintf("add element inet stayconnect %s { %s timeout %ds }", Phase3AuthV4, key, it.secs))
			if it.mac != "" && snap.macErr == nil {
				for _, e := range snap.mac[it.ip4.String()] {
					if e.Iface == r.Iface {
						lines = append(lines, fmt.Sprintf("delete element inet stayconnect %s { %s }", Phase3MACV4, macElemKey(r.Iface, it.ip4, e.MAC)))
					}
				}
				lines = append(lines, fmt.Sprintf("add element inet stayconnect %s { %s timeout %ds }", Phase3MACV4, macElemKey(r.Iface, it.ip4, it.mac), it.secs))
			}
		}
		if werr = c.runTxn(ctx, lines); werr == nil {
			break
		}
	}
	if werr != nil {
		// Attribute the failure: the per-element calls say which request cannot be written.
		for _, it := range items {
			if last[reqKey(reqs[it.i].Iface, it.ip4)] != it.i {
				continue
			}
			errs[it.i] = c.leaseOne(ctx, reqs[it.i], requireMAC)
		}
		for _, it := range items {
			if l := last[reqKey(reqs[it.i].Iface, it.ip4)]; l != it.i {
				errs[it.i] = errs[l]
			}
		}
		return
	}
	// Verify what the guest's forwarding depends on: the element is there after the write.
	snap, err := c.snapshot(ctx, requireMAC, requireMAC)
	concat := c.probeConcatIn(ctx, Phase3AuthV4)
	for _, it := range items {
		r := reqs[it.i]
		if err != nil {
			errs[it.i] = err
			continue
		}
		if !c.ipPresent(snap, concat, r.Iface, it.ip4) {
			errs[it.i] = fmt.Errorf("nft: authorization for %s on %s is not present after the batch write", it.ip4, r.Iface)
			continue
		}
		if requireMAC && it.mac != "" && !macPresent(snap, r.Iface, it.ip4, it.mac) {
			errs[it.i] = fmt.Errorf("nft: MAC-bound authorization for %s on %s is not present after the batch write", it.ip4, r.Iface)
		}
	}
}

func macPresent(s setSnapshot, iface string, ip4 net.IP, mac string) bool {
	for _, e := range s.mac[ip4.String()] {
		if e.Iface == iface && strings.EqualFold(e.MAC, mac) {
			return true
		}
	}
	return false
}

// leaseOne is the per-element path, with the semantics the gate had before batching.
func (c *Client) leaseOne(ctx context.Context, r LeaseReq, requireMAC bool) error {
	if err := c.LeaseIn(ctx, Phase3AuthV4, r.Iface, r.IP, r.TTL); err != nil {
		return err
	}
	if r.MAC == "" {
		return nil
	}
	if err := c.LeaseMAC(ctx, r.Iface, r.IP, r.MAC, r.TTL); err != nil && requireMAC {
		return err
	}
	return nil
}

// RevokeBatch removes the authorizations of reqs (IP-only element and every MAC-bound element of the address) and proves they are gone. The
// result has one entry per request; nil means removed and verified absent. With requireMAC false a leftover MAC-bound element is tolerated, as
// DenyMAC's failure is while MAC binding is off.
func (c *Client) RevokeBatch(ctx context.Context, reqs []AddrReq, requireMAC bool) []error {
	errs := make([]error, len(reqs))
	for start := 0; start < len(reqs); start += BatchChunk {
		end := start + BatchChunk
		if end > len(reqs) {
			end = len(reqs)
		}
		c.revokeChunk(ctx, reqs[start:end], errs[start:end], requireMAC)
	}
	return errs
}

func (c *Client) revokeChunk(ctx context.Context, reqs []AddrReq, errs []error, requireMAC bool) {
	type item struct {
		i   int
		ip4 net.IP
	}
	var items []item
	seen := map[string]int{}
	for i, r := range reqs {
		ip4 := r.IP.To4()
		if ip4 == nil {
			errs[i] = fmt.Errorf("only IPv4 supported: %s", r.IP)
			continue
		}
		seen[reqKey(r.Iface, ip4)] = i
		items = append(items, item{i, ip4})
	}
	if len(items) == 0 {
		return
	}
	var werr error
	var snap setSnapshot
	for attempt := 0; attempt < 2; attempt++ {
		var err error
		snap, err = c.snapshot(ctx, true, requireMAC)
		if err != nil {
			werr = err
			break
		}
		concat := c.probeConcatIn(ctx, Phase3AuthV4)
		var lines []string
		for _, it := range items {
			r := reqs[it.i]
			if seen[reqKey(r.Iface, it.ip4)] != it.i {
				continue
			}
			for _, e := range snap.ip[it.ip4.String()] {
				if concat && r.Iface != "" && e.Iface != r.Iface {
					continue
				}
				key := it.ip4.String()
				if concat && (r.Iface != "" || e.Iface != "") {
					useIf := r.Iface
					if useIf == "" {
						useIf = e.Iface
					}
					key = fmt.Sprintf("%q . %s", useIf, it.ip4.String())
				}
				lines = append(lines, fmt.Sprintf("delete element inet stayconnect %s { %s }", Phase3AuthV4, key))
			}
			for _, e := range snap.mac[it.ip4.String()] {
				if (r.Iface == "" || e.Iface == r.Iface) && e.MAC != "" {
					lines = append(lines, fmt.Sprintf("delete element inet stayconnect %s { %s }", Phase3MACV4, macElemKey(e.Iface, it.ip4, e.MAC)))
				}
			}
		}
		if len(lines) == 0 {
			werr = nil // nothing to remove: the read just taken is the proof
			break
		}
		if werr = c.runTxn(ctx, lines); werr == nil {
			snap = setSnapshot{} // the post-write state must be read afresh
			break
		}
	}
	if werr != nil {
		for _, it := range items {
			if seen[reqKey(reqs[it.i].Iface, it.ip4)] != it.i {
				continue
			}
			errs[it.i] = c.revokeOne(ctx, reqs[it.i], requireMAC)
		}
		for _, it := range items {
			if l := seen[reqKey(reqs[it.i].Iface, it.ip4)]; l != it.i {
				errs[it.i] = errs[l]
			}
		}
		return
	}
	if snap.ip == nil {
		var err error
		snap, err = c.snapshot(ctx, requireMAC, requireMAC)
		if err != nil {
			for _, it := range items {
				errs[it.i] = err
			}
			return
		}
	}
	concat := c.probeConcatIn(ctx, Phase3AuthV4)
	for _, it := range items {
		r := reqs[it.i]
		if c.ipPresent(snap, concat, r.Iface, it.ip4) {
			errs[it.i] = ErrStillPresent
			continue
		}
		if requireMAC {
			for _, e := range snap.mac[it.ip4.String()] {
				if r.Iface == "" || e.Iface == r.Iface {
					errs[it.i] = ErrStillPresent
					break
				}
			}
		}
	}
}

// revokeOne is the per-element path, with the semantics the gate had before batching: remove both elements, then prove the address is not authorized.
func (c *Client) revokeOne(ctx context.Context, r AddrReq, requireMAC bool) error {
	err := c.DenyIn(ctx, Phase3AuthV4, r.Iface, r.IP)
	if merr := c.DenyMAC(ctx, r.Iface, r.IP); merr != nil && err == nil && requireMAC {
		err = merr
	}
	if err != nil {
		return err
	}
	still, err := c.AuthorizedIn(ctx, Phase3AuthV4, r.Iface, r.IP)
	if err != nil {
		return err
	}
	if still {
		return ErrStillPresent
	}
	return nil
}

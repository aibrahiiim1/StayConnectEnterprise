package main

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/nft"
)

// fakeSets is a map-backed stand-in for the two Phase-3 sets.
type fakeSets struct {
	ip      map[string]time.Duration // "br|ip"
	mac     map[string]string        // "br|ip" -> mac
	macErr  error
	macLeft map[string]time.Duration
}

func newFakeSets() *fakeSets {
	return &fakeSets{ip: map[string]time.Duration{}, mac: map[string]string{}, macLeft: map[string]time.Duration{}}
}

func (f *fakeSets) LeaseIn(_ context.Context, _ string, iface string, ip net.IP, ttl time.Duration) error {
	f.ip[gateKey(iface, ip)] = ttl
	return nil
}
func (f *fakeSets) DenyIn(_ context.Context, _ string, iface string, ip net.IP) error {
	delete(f.ip, gateKey(iface, ip))
	return nil
}
func (f *fakeSets) AuthorizedIn(_ context.Context, _ string, iface string, ip net.IP) (bool, error) {
	_, ok := f.ip[gateKey(iface, ip)]
	return ok, nil
}
func (f *fakeSets) ListIn(_ context.Context, set string) ([]nft.Element, error) {
	var out []nft.Element
	if set == nft.Phase3MACV4 {
		if f.macErr != nil {
			return nil, f.macErr
		}
		for k, m := range f.mac {
			br, ip := splitKey(k)
			out = append(out, nft.Element{Iface: br, IP: net.ParseIP(ip), MAC: m, Expires: f.macLeft[k]})
		}
		return out, nil
	}
	for k, ttl := range f.ip {
		br, ip := splitKey(k)
		out = append(out, nft.Element{Iface: br, IP: net.ParseIP(ip), Expires: ttl})
	}
	return out, nil
}
func (f *fakeSets) LeaseMAC(_ context.Context, iface string, ip net.IP, mac string, ttl time.Duration) error {
	if f.macErr != nil {
		return f.macErr
	}
	f.mac[gateKey(iface, ip)] = mac
	f.macLeft[gateKey(iface, ip)] = ttl
	return nil
}
func (f *fakeSets) DenyMAC(_ context.Context, iface string, ip net.IP) error {
	if f.macErr != nil {
		return f.macErr
	}
	delete(f.mac, gateKey(iface, ip))
	return nil
}

func splitKey(k string) (string, string) {
	for i := 0; i < len(k); i++ {
		if k[i] == '|' {
			return k[:i], k[i+1:]
		}
	}
	return k, ""
}

func newMACGate(on bool) (*nftGate, *fakeSets) {
	f := newFakeSets()
	return &nftGate{c: f, macOn: func() bool { return on }}, f
}

var (
	gIP  = net.ParseIP("10.20.0.5")
	gCtx = context.Background()
)

func TestAuthorizeKeepsBothSetsWarmWhateverTheSwitch(t *testing.T) {
	for _, on := range []bool{false, true} {
		g, f := newMACGate(on)
		g.BindMAC("br-g20", gIP, "AA:BB:CC:DD:EE:01")
		if err := g.Authorize(gCtx, "br-g20", gIP, time.Minute); err != nil {
			t.Fatal(err)
		}
		if _, ok := f.ip["br-g20|10.20.0.5"]; !ok {
			t.Fatalf("on=%v: the IP-only set (the fallback) is cold", on)
		}
		if f.mac["br-g20|10.20.0.5"] != "aa:bb:cc:dd:ee:01" {
			t.Fatalf("on=%v: the MAC-bound set is not populated: %v", on, f.mac)
		}
	}
}

func TestADeviceWithNoKnownMACIsNotAuthorizedWhenBindingIsOn(t *testing.T) {
	g, f := newMACGate(true)
	if err := g.Authorize(gCtx, "br-g20", gIP, time.Minute); err != nil {
		t.Fatal(err)
	}
	if len(f.mac) != 0 {
		t.Fatal("a MAC element was invented")
	}
	ok, err := g.Authorized(gCtx, "br-g20", gIP)
	if err != nil || ok {
		t.Fatalf("with binding on an address without a MAC must not verify as authorized (ok=%v err=%v)", ok, err)
	}
	// With binding off the same device is authorized exactly as before (the emergency fallback).
	g2, _ := newMACGate(false)
	_ = g2.Authorize(gCtx, "br-g20", gIP, time.Minute)
	if ok, _ := g2.Authorized(gCtx, "br-g20", gIP); !ok {
		t.Fatal("the fallback lost IP-only authorization")
	}
}

func TestAMACWriteFailureIsFatalOnlyWhileBindingIsOn(t *testing.T) {
	g, f := newMACGate(false)
	f.macErr = errors.New("no such set")
	g.BindMAC("br-g20", gIP, "aa:bb:cc:dd:ee:01")
	if err := g.Authorize(gCtx, "br-g20", gIP, time.Minute); err != nil {
		t.Fatalf("an older table without the MAC set must not break IP-only authorization: %v", err)
	}
	g2, f2 := newMACGate(true)
	f2.macErr = errors.New("no such set")
	g2.BindMAC("br-g20", gIP, "aa:bb:cc:dd:ee:01")
	if err := g2.Authorize(gCtx, "br-g20", gIP, time.Minute); err == nil {
		t.Fatal("with binding on a failed MAC write must fail closed")
	}
}

func TestRevokeRemovesBothAndForgetsTheMAC(t *testing.T) {
	g, f := newMACGate(true)
	g.BindMAC("br-g20", gIP, "aa:bb:cc:dd:ee:01")
	_ = g.Authorize(gCtx, "br-g20", gIP, time.Minute)
	if err := g.Revoke(gCtx, "br-g20", gIP); err != nil {
		t.Fatal(err)
	}
	if len(f.ip) != 0 || len(f.mac) != 0 || g.macFor("br-g20", gIP) != "" {
		t.Fatalf("revocation left state behind: ip=%v mac=%v", f.ip, f.mac)
	}
	f.macErr = errors.New("nft busy")
	_ = g.Authorize(gCtx, "br-g20", gIP, time.Minute) // macs is empty now, so no MAC write
	g.BindMAC("br-g20", gIP, "aa:bb:cc:dd:ee:01")
	if err := g.Revoke(gCtx, "br-g20", gIP); err == nil {
		t.Fatal("with binding on, a MAC element that could not be removed means revocation is NOT proven")
	}
}

func TestListFlagsAMissingMACElementForRenewalAndSurfacesOrphans(t *testing.T) {
	g, f := newMACGate(true)
	g.BindMAC("br-g20", gIP, "aa:bb:cc:dd:ee:01")
	f.ip["br-g20|10.20.0.5"] = 80 * time.Second // healthy IP element, no MAC element yet (e.g. just enabled)
	f.mac["br-g20|10.20.0.9"] = "aa:bb:cc:dd:ee:09"
	f.macLeft["br-g20|10.20.0.9"] = 30 * time.Second
	els, err := g.List(gCtx)
	if err != nil {
		t.Fatal(err)
	}
	var renew, orphan bool
	for _, e := range els {
		if e.IP.Equal(gIP) && e.Expires == 0 {
			renew = true
		}
		if e.IP.Equal(net.ParseIP("10.20.0.9")) {
			orphan = true
		}
	}
	if !renew || !orphan {
		t.Fatalf("renew=%v orphan=%v: %+v", renew, orphan, els)
	}
	f.macErr = errors.New("unreadable")
	if _, err := g.List(gCtx); err == nil {
		t.Fatal("with binding on, an unreadable MAC set is unknown, not empty")
	}
	g.macOn = func() bool { return false }
	if els, err := g.List(gCtx); err != nil || len(els) != 1 {
		t.Fatalf("binding off must keep the IP-only view alive: %v %v", els, err)
	}
}

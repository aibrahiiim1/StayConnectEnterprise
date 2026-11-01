package portaltls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseAndCheckSharedCSR(t *testing.T) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	shared, own := DefaultSharedHostname, "abcdefgh.gw.onegate.work"
	for name, req := range map[string]x509.CertificateRequest{
		"shared first": {DNSNames: []string{shared, own}},
		"own first":    {DNSNames: []string{own, shared}},
		"CN shared":    {DNSNames: []string{shared, own}, Subject: pkix.Name{CommonName: shared}},
		"CN own":       {DNSNames: []string{shared, own}, Subject: pkix.Name{CommonName: own}},
	} {
		if _, err := ParseAndCheckSharedCSR(csrPEM(t, k, req), shared, own); err != nil {
			t.Errorf("%s: must pass: %v", name, err)
		}
	}
	for name, p := range map[string]string{
		"shared only":       csrPEM(t, k, x509.CertificateRequest{DNSNames: []string{shared}}),
		"another's name":    csrPEM(t, k, x509.CertificateRequest{DNSNames: []string{shared, "zzzzzzzz.gw.onegate.work"}}),
		"three names":       csrPEM(t, k, x509.CertificateRequest{DNSNames: []string{shared, own, "x.example.com"}}),
		"same name twice":   csrPEM(t, k, x509.CertificateRequest{DNSNames: []string{shared, shared}}),
		"CN is a 3rd name":  csrPEM(t, k, x509.CertificateRequest{DNSNames: []string{shared, own}, Subject: pkix.Name{CommonName: "evil.example.com"}}),
		"P-384":             csrPEM(t, p384, x509.CertificateRequest{DNSNames: []string{shared, own}}),
		"uppercase variant": csrPEM(t, k, x509.CertificateRequest{DNSNames: []string{strings.ToUpper(shared), own}}),
		"not a CSR":         "hello",
	} {
		if _, err := ParseAndCheckSharedCSR(p, shared, own); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

func TestSharedOptionsAndNames(t *testing.T) {
	if (Options{}).Shared().Enabled() {
		t.Fatal("unset options must leave the shared name off (existing tests and old configurations)")
	}
	o, _ := OptionsFromEnv()
	sp := o.Shared()
	if !sp.Enabled() || sp.Hostname != DefaultSharedHostname || sp.Address.String() != DefaultSharedAddress {
		t.Fatalf("the product default is the shared name on 10.255.255.1: %+v", sp)
	}
	if (Options{SharedHostname: "off", SharedAddress: DefaultSharedAddress}).Shared().Enabled() {
		t.Fatal("off switches it off")
	}
	if (Options{SharedHostname: DefaultSharedHostname, SharedAddress: "8.8.8.8"}).Shared().Enabled() {
		t.Fatal("a public address is never the shared portal address")
	}
	if err := (Options{DNSProvider: ProviderCloudflare, SharedHostname: "portal", SharedAddress: DefaultSharedAddress}).Check(); err == nil {
		t.Fatal("a single-label shared name is refused")
	}
	if err := (Options{DNSProvider: ProviderCloudflare, SharedHostname: DefaultSharedHostname, SharedAddress: "1.1.1.1"}).Check(); err == nil {
		t.Fatal("a public shared address is refused")
	}
	for name, want := range map[string]bool{
		DefaultSharedHostname: true, "x." + DefaultSharedHostname: true, "PORTAL.onegate.getsemantics.com.": true,
		"central.onegate.getsemantics.com": false, "wifi.nilehotel.com": false, "xportal.onegate.getsemantics.com": false,
	} {
		if got := sp.IsSharedName(name); got != want {
			t.Errorf("IsSharedName(%q) = %v, want %v", name, got, want)
		}
	}
	if SharedChallengeName("onegate.work") != "sharedportal.acme.onegate.work" {
		t.Fatal("delegate name")
	}
	if len(SharedChallengeLabel) == challengeLabelLen {
		t.Fatal("the shared delegate label must never have a hotel delegate label's length")
	}
	recs := SharedRecords(sp, "onegate.work")
	if len(recs) != 2 || recs[0] != (Record{Type: "A", Name: DefaultSharedHostname, Value: DefaultSharedAddress}) ||
		recs[1] != (Record{Type: "CNAME", Name: "_acme-challenge." + DefaultSharedHostname, Value: "sharedportal.acme.onegate.work."}) {
		t.Fatalf("records: %+v", recs)
	}
}

func TestCheckShared(t *testing.T) {
	sp := SharedPortal{Hostname: DefaultSharedHostname}
	sp.Address, _ = ParsePortalIP(DefaultSharedAddress)
	run := func(setup func(d *mutableDoH)) error {
		d := &mutableDoH{table: map[string][]dohAnswer{}}
		setup(d)
		srv := httptest.NewServer(d)
		defer srv.Close()
		return CheckShared(context.Background(), DoHResolver{URL: srv.URL}, sp, "onegate.work", "")
	}
	ready := func(d *mutableDoH) {
		d.set(sp.Hostname, rrA, DefaultSharedAddress)
		d.set("_acme-challenge."+sp.Hostname, rrCNAME, "sharedportal.acme.onegate.work.")
	}
	if err := run(ready); err != nil {
		t.Fatalf("both records published: %v", err)
	}
	cases := map[string]func(d *mutableDoH){
		"no A record": func(d *mutableDoH) {
			d.set("_acme-challenge."+sp.Hostname, rrCNAME, "sharedportal.acme.onegate.work.")
		},
		"the old address": func(d *mutableDoH) {
			d.set(sp.Hostname, rrA, "10.20.0.1")
			d.set("_acme-challenge."+sp.Hostname, rrCNAME, "sharedportal.acme.onegate.work.")
		},
		"an extra address": func(d *mutableDoH) {
			ready(d)
			d.set(sp.Hostname, rrA, "10.20.0.1")
		},
		"no delegation": func(d *mutableDoH) { d.set(sp.Hostname, rrA, DefaultSharedAddress) },
		"wrong delegate": func(d *mutableDoH) {
			d.set(sp.Hostname, rrA, DefaultSharedAddress)
			d.set("_acme-challenge."+sp.Hostname, rrCNAME, "0123456789abcdef.acme.onegate.work.")
		},
		"CAA forbids": func(d *mutableDoH) {
			ready(d)
			d.set("getsemantics.com", rrCAA, `0 issue "digicert.com"`)
		},
	}
	for name, setup := range cases {
		if err := run(setup); err == nil {
			t.Errorf("%s: must not be ready", name)
		}
	}
	if err := run(func(d *mutableDoH) {
		ready(d)
		d.set("getsemantics.com", rrCAA, `0 issue "letsencrypt.org"`)
	}); err != nil {
		t.Fatalf("a CAA that names Let's Encrypt permits: %v", err)
	}
}

func TestSameNames(t *testing.T) {
	if !sameNames([]string{"a.example.com", "b.example.com"}, []string{"b.example.com", "a.example.com"}) {
		t.Fatal("order does not matter")
	}
	if sameNames([]string{"a.example.com"}, []string{"a.example.com", "b.example.com"}) ||
		sameNames([]string{"a.example.com", "a.example.com"}, []string{"a.example.com", "b.example.com"}) {
		t.Fatal("the sets must be equal")
	}
}

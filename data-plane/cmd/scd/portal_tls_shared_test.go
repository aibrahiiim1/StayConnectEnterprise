package main

// The shared Client Portal name: one public name for every appliance, each with its own key and certificate.

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"sort"
	"strings"
	"testing"
)

const sharedHost = "portal.onegate.getsemantics.com"

func offerShared(f *agentFixture, ready bool) {
	f.central.shared = &ptShared{Hostname: sharedHost, Address: "10.255.255.1", Eligible: true, Ready: ready}
}

func csrNames(t *testing.T, csrPEM []byte) []string {
	t.Helper()
	b, _ := pem.Decode(csrPEM)
	if b == nil {
		t.Fatal("not PEM")
	}
	c, err := x509.ParseCertificateRequest(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	n := append([]string{}, c.DNSNames...)
	sort.Strings(n)
	return n
}

func TestTheSharedNameIsOrderedWithTheOwnHostnameAndBecomesCanonical(t *testing.T) {
	f := newAgentFixture(t)
	offerShared(f, true)
	st := f.cycle(t, false)
	if st.HTTPSActive {
		t.Fatalf("nothing is installed yet: %+v", st)
	}
	var sharedOrder *fakeOrder
	for _, o := range f.central.orders {
		if o.host == sharedHost {
			sharedOrder = o
		}
	}
	if sharedOrder == nil || f.repo.certs[sharedHost] == nil || f.repo.certs[sharedHost].Kind != ptKindOneGate {
		t.Fatalf("the shared name is ordered as a OneGate-kind certificate: %+v", f.repo.certs)
	}
	if got := strings.Join(csrNames(t, sharedOrder.csr), ","); got != ogHost+","+sharedHost {
		t.Fatalf("the shared-name CSR names exactly the shared name and the own hostname: %s", got)
	}
	st = f.cycle(t, false)
	if !st.HTTPSActive || st.Canonical != sharedHost || st.SharedHostname != sharedHost {
		t.Fatalf("with its certificate installed the shared name is canonical: %+v", st)
	}
	if len(st.Hostnames) != 2 || st.Hostnames[0] != sharedHost || st.Hostnames[1] != ogHost {
		t.Fatalf("both names are served: %v", st.Hostnames)
	}
	sharedKey, _ := f.store.Inspect(sharedHost)
	ownKey, _ := f.store.Inspect(ogHost)
	if sharedKey == nil || ownKey == nil || sharedKey.Fingerprint == ownKey.Fingerprint {
		t.Fatal("two certificates, each installed under its own name")
	}

	// Central unreachable: the shared name stays in use and canonical.
	f.central.unreachable = true
	st = f.cycle(t, false)
	if !st.HTTPSActive || st.Canonical != sharedHost || st.SharedHostname != sharedHost || f.repo.certs[sharedHost] == nil {
		t.Fatalf("an unreachable Central takes nothing down: %+v", st)
	}
	f.central.unreachable = false

	// Central withdraws the name (the address changed, or the feature was switched off): it stops being served.
	f.central.shared.Eligible = false
	st = f.cycle(t, false)
	if st.Canonical != ogHost || st.SharedHostname != "" || f.repo.certs[sharedHost] != nil {
		t.Fatalf("a withdrawn shared name falls back to the own hostname: %+v %v", st, f.repo.certs[sharedHost])
	}
	if _, err := f.store.Inspect(sharedHost); err == nil {
		t.Fatal("the withdrawn name's files are removed")
	}
}

func TestTheSharedNameIsNotOrderedUntilPublicDNSIsReady(t *testing.T) {
	f := newAgentFixture(t)
	offerShared(f, false)
	f.cycle(t, false)
	st := f.cycle(t, false)
	if st.Canonical != ogHost {
		t.Fatalf("the own hostname serves meanwhile: %+v", st)
	}
	for _, o := range f.central.orders {
		if o.host == sharedHost {
			t.Fatal("no shared-name order before Central says public DNS is ready")
		}
	}
	if r := f.repo.certs[sharedHost]; r == nil || r.PendingOrderID != "" {
		t.Fatalf("the row exists (so local DNS answers the name) but holds no order: %+v", r)
	}
	f.central.shared.Ready = true
	f.cycle(t, false)
	st = f.cycle(t, false)
	if st.Canonical != sharedHost {
		t.Fatalf("once ready the shared name is ordered and becomes canonical: %+v", st)
	}
}

func TestTheSharedNameNeedsTheSharedAddress(t *testing.T) {
	f := newAgentFixture(t)
	f.repo.set.PortalIP = "10.254.254.1"
	offerShared(f, true) // a Central that wrongly says eligible for another address
	f.cycle(t, false)
	st := f.cycle(t, false)
	if st.SharedHostname != "" || st.Canonical != ogHost || f.repo.certs[sharedHost] != nil {
		t.Fatalf("an appliance whose portal address is not the shared one never uses the shared name: %+v", st)
	}
}

func TestTheSharedNameIsPreferredOverAHotelDomain(t *testing.T) {
	f := newAgentFixture(t)
	f.repo.set.CustomDomain = customHost
	f.central.domain = &ptCustomDomain{FQDN: customHost, Status: "VERIFIED"}
	offerShared(f, true)
	f.cycle(t, false)
	st := f.cycle(t, false)
	if st.Canonical != sharedHost {
		t.Fatalf("one Client Portal name for every appliance: %+v", st)
	}
	found := false
	for _, h := range st.Hostnames {
		found = found || h == customHost
	}
	if !found {
		t.Fatalf("the hotel domain is still served (and redirected to the canonical name): %v", st.Hostnames)
	}
}

// Review finding (PR #283): Central unreachable AND the certificate rows unreadable in one cycle must not delete
// the shared name's files or stop serving it.
func TestUnreadableRecordsAndNoCentralKeepTheSharedName(t *testing.T) {
	f := newAgentFixture(t)
	offerShared(f, true)
	f.cycle(t, false)
	if st := f.cycle(t, false); st.Canonical != sharedHost {
		t.Fatalf("precondition: %+v", st)
	}
	f.central.unreachable = true
	f.repo.certsErr = errors.New("connection reset")
	st := f.cycle(t, false)
	if _, err := f.store.Inspect(sharedHost); err != nil {
		t.Fatalf("the shared name's certificate must survive: %v", err)
	}
	if !st.HTTPSActive || st.Canonical != sharedHost || st.SharedHostname != sharedHost {
		t.Fatalf("it is still served: %+v", st)
	}
	f.repo.certsErr = nil
	f.central.unreachable = false
	if st = f.cycle(t, false); st.Canonical != sharedHost {
		t.Fatalf("and stays canonical once both recover: %+v", st)
	}
}
